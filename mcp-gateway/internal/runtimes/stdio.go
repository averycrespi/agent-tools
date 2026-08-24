package runtimes

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
)

var (
	ErrStdioInvalidDefinition = errors.New("stdio runtime definition is invalid")
	ErrStdioInvalidSecrets    = errors.New("stdio runtime secrets are invalid")
	ErrStdioResourceLimit     = errors.New("stdio runtime limit is reached")
	ErrStdioStartFailed       = errors.New("stdio runtime failed to start")
	stdioSecretSlotPattern    = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
)

type StdioDefinition struct {
	RuntimeID         string
	Executable        string
	Arguments         []string
	WorkingDirectory  string
	Environment       map[string]string
	SecretEnvironment map[string]string
	Secrets           map[string]string
}

type StdioExit struct {
	Reason    contract.PublicReason
	Retryable bool
}

type StdioSupervisor struct {
	mu           sync.Mutex
	runtimes     map[string]*StdioRuntime
	now          func() time.Time
	after        func(time.Duration) <-chan time.Time
	captureGroup func(*os.Process) (int, bool)
	signalGroup  func(*os.Process, int, bool) bool
	limit        int64
}

type StdioRuntime struct {
	mu           sync.Mutex
	supervisor   *StdioSupervisor
	id           string
	command      *exec.Cmd
	processGroup int
	stdin        io.WriteCloser
	stdout       io.ReadCloser
	stderr       io.ReadCloser
	frames       chan []byte
	done         chan StdioExit
	finished     chan struct{}
	cancel       context.CancelFunc
	failure      *contract.PublicReason
	diagnostics  []byte
	exited       bool
	stopMu       sync.Mutex
}

type byteRateLimiter struct {
	mu       sync.Mutex
	now      func() time.Time
	last     time.Time
	tokens   float64
	rate     float64
	capacity float64
}

func NewStdioSupervisor(now func() time.Time) *StdioSupervisor {
	if now == nil {
		now = time.Now
	}
	limit, _ := contract.FixedLimitByName("downstream_runtimes")
	return &StdioSupervisor{runtimes: make(map[string]*StdioRuntime), now: now, after: time.After, captureGroup: captureStdioProcessGroup, signalGroup: signalStdioProcessGroup, limit: limit.Maximum}
}

func (supervisor *StdioSupervisor) Start(ctx context.Context, definition StdioDefinition) (*StdioRuntime, error) {
	if err := validateStdioDefinition(definition); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil || !stdioProcessGroupsSupported() {
		return nil, ErrStdioStartFailed
	}
	supervisor.mu.Lock()
	if _, exists := supervisor.runtimes[definition.RuntimeID]; exists || int64(len(supervisor.runtimes)) >= supervisor.limit {
		supervisor.mu.Unlock()
		return nil, ErrStdioResourceLimit
	}
	supervisor.runtimes[definition.RuntimeID] = nil
	supervisor.mu.Unlock()

	command := exec.Command(definition.Executable, definition.Arguments...) //nolint:gosec // validated administrator-owned absolute executable, invoked directly without a shell or PATH lookup.
	command.Dir = definition.WorkingDirectory
	command.Env = buildStdioEnvironment(definition)
	configureStdioProcess(command)
	stdin, err := command.StdinPipe()
	if err != nil {
		supervisor.release(definition.RuntimeID, nil)
		return nil, ErrStdioStartFailed
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		supervisor.release(definition.RuntimeID, nil)
		return nil, ErrStdioStartFailed
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		supervisor.release(definition.RuntimeID, nil)
		return nil, ErrStdioStartFailed
	}
	if err := command.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		_ = stderr.Close()
		supervisor.release(definition.RuntimeID, nil)
		return nil, ErrStdioStartFailed
	}

	groupID, verified := supervisor.captureGroup(command.Process)
	if !verified {
		_ = stdin.Close()
		_ = stdout.Close()
		_ = stderr.Close()
		_ = command.Process.Kill()
		_, _ = command.Process.Wait()
		supervisor.release(definition.RuntimeID, nil)
		return nil, ErrStdioStartFailed
	}
	runtimeCtx, cancel := context.WithCancel(context.Background())
	runtime := &StdioRuntime{supervisor: supervisor, id: definition.RuntimeID, command: command, processGroup: groupID, stdin: stdin, stdout: stdout, stderr: stderr, frames: make(chan []byte, 16), done: make(chan StdioExit, 1), finished: make(chan struct{}), cancel: cancel}
	supervisor.mu.Lock()
	supervisor.runtimes[definition.RuntimeID] = runtime
	supervisor.mu.Unlock()
	runtime.supervise(runtimeCtx)
	go func() {
		select {
		case <-ctx.Done():
			runtime.StopNow()
		case <-runtime.finished:
		}
	}()
	return runtime, nil
}

func (supervisor *StdioSupervisor) Status() contract.LimitStatus {
	supervisor.mu.Lock()
	defer supervisor.mu.Unlock()
	inUse := int64(len(supervisor.runtimes))
	return contract.LimitStatus{InUse: inUse, Limit: supervisor.limit, Saturated: inUse >= supervisor.limit}
}

func (supervisor *StdioSupervisor) release(runtimeID string, runtime *StdioRuntime) {
	supervisor.mu.Lock()
	defer supervisor.mu.Unlock()
	if current, exists := supervisor.runtimes[runtimeID]; exists && current == runtime {
		delete(supervisor.runtimes, runtimeID)
	}
}

func (runtime *StdioRuntime) Frames() <-chan []byte  { return runtime.frames }
func (runtime *StdioRuntime) Done() <-chan StdioExit { return runtime.done }
func (runtime *StdioRuntime) Input() io.WriteCloser  { return runtime.stdin }

func (runtime *StdioRuntime) CloseInput() error { return runtime.stdin.Close() }

func (runtime *StdioRuntime) Stop(ctx context.Context) bool {
	runtime.stopMu.Lock()
	defer runtime.stopMu.Unlock()
	if runtime.hasExited() {
		return true
	}
	runtime.cancel()
	_ = runtime.stdin.Close()
	if !runtime.supervisor.signalGroup(runtime.command.Process, runtime.processGroup, false) {
		return runtime.hasExited()
	}
	if runtime.waitForExit(ctx, contract.StdioGracefulStopDeadline) {
		return true
	}
	if !runtime.supervisor.signalGroup(runtime.command.Process, runtime.processGroup, true) {
		return runtime.hasExited()
	}
	return runtime.waitForExit(ctx, contract.StdioForcedStopDeadline)
}

func (runtime *StdioRuntime) StopNow() {
	go runtime.Stop(context.Background())
}

func (runtime *StdioRuntime) hasExited() bool {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	return runtime.exited
}

func (runtime *StdioRuntime) waitForExit(ctx context.Context, timeout time.Duration) bool {
	select {
	case <-runtime.finished:
		return true
	default:
	}
	select {
	case <-runtime.finished:
		return true
	case <-runtime.supervisor.after(timeout):
		return false
	case <-ctx.Done():
		return false
	}
}

func (runtime *StdioRuntime) supervise(ctx context.Context) {
	stdoutDone := make(chan struct{})
	stderrDone := make(chan struct{})
	stdoutRate := newByteRateLimiter(runtime.supervisor.now)
	stderrRate := newByteRateLimiter(runtime.supervisor.now)
	go func() {
		defer close(stdoutDone)
		runtime.readFrames(ctx, stdoutRate)
	}()
	go func() {
		defer close(stderrDone)
		runtime.readStderr(ctx, stderrRate)
	}()
	go func() {
		_, _ = runtime.command.Process.Wait()
		_ = runtime.stdin.Close()
		runtime.mu.Lock()
		runtime.exited = true
		runtime.mu.Unlock()
		<-stdoutDone
		<-stderrDone
		runtime.cancel()
		_ = runtime.stdout.Close()
		_ = runtime.stderr.Close()
		runtime.mu.Lock()
		reason := contract.ReasonProcessExited
		if runtime.failure != nil {
			reason = *runtime.failure
		}
		runtime.mu.Unlock()
		runtime.supervisor.release(runtime.id, runtime)
		close(runtime.frames)
		runtime.done <- StdioExit{Reason: reason, Retryable: true}
		close(runtime.done)
		close(runtime.finished)
	}()
}

func (runtime *StdioRuntime) readFrames(ctx context.Context, limiter *byteRateLimiter) {
	maximum := int(limitByName("stdio_protocol_frame_bytes"))
	reader := bufio.NewReaderSize(runtime.stdout, maximum+1)
	for {
		line, err := reader.ReadSlice('\n')
		if !limiter.Allow(len(line)) {
			runtime.fail(contract.ReasonOutputLimit)
			return
		}
		if errors.Is(err, bufio.ErrBufferFull) || (err != nil && len(line) > maximum) {
			runtime.fail(contract.ReasonOutputLimit)
			return
		}
		if len(line) != 0 {
			if line[len(line)-1] != '\n' {
				if err != nil {
					runtime.fail(contract.ReasonProtocolInvalid)
				}
				return
			}
			line = line[:len(line)-1]
			if len(line) > maximum {
				runtime.fail(contract.ReasonOutputLimit)
				return
			}
			frame := append([]byte(nil), line...)
			select {
			case runtime.frames <- frame:
			case <-ctx.Done():
				return
			default:
				runtime.fail(contract.ReasonOutputLimit)
				return
			}
		}
		if err != nil {
			return
		}
	}
}

func (runtime *StdioRuntime) readStderr(ctx context.Context, limiter *byteRateLimiter) {
	maximum := int(limitByName("stdio_stderr_bytes"))
	buffer := make([]byte, 32*1024)
	for {
		count, err := runtime.stderr.Read(buffer)
		if count > 0 {
			if !limiter.Allow(count) {
				runtime.fail(contract.ReasonOutputLimit)
				return
			}
			runtime.mu.Lock()
			remaining := maximum - len(runtime.diagnostics)
			if remaining > count {
				remaining = count
			}
			if remaining > 0 {
				runtime.diagnostics = append(runtime.diagnostics, buffer[:remaining]...)
			}
			runtime.mu.Unlock()
		}
		if err != nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		default:
		}
	}
}

func (runtime *StdioRuntime) fail(reason contract.PublicReason) {
	runtime.mu.Lock()
	if runtime.failure == nil {
		runtime.failure = &reason
	}
	runtime.mu.Unlock()
	runtime.StopNow()
}

func newByteRateLimiter(now func() time.Time) *byteRateLimiter {
	rate := float64(limitByName("stdio_output_rate_bytes_per_second"))
	burst := float64(limitByName("stdio_output_burst_bytes"))
	current := now()
	return &byteRateLimiter{now: now, last: current, tokens: burst, rate: rate, capacity: burst}
}

func (limiter *byteRateLimiter) Allow(count int) bool {
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	now := limiter.now()
	elapsed := now.Sub(limiter.last).Seconds()
	if elapsed > 0 {
		limiter.tokens += elapsed * limiter.rate
		if limiter.tokens > limiter.capacity {
			limiter.tokens = limiter.capacity
		}
		limiter.last = now
	}
	if float64(count) > limiter.tokens {
		return false
	}
	limiter.tokens -= float64(count)
	return true
}

func validateStdioDefinition(definition StdioDefinition) error {
	if definition.RuntimeID == "" || !absoluteCleanPath(definition.Executable) || !absoluteCleanPath(definition.WorkingDirectory) || int64(len(definition.Executable)) > limitByName("stdio_path_bytes") || int64(len(definition.WorkingDirectory)) > limitByName("stdio_path_bytes") {
		return ErrStdioInvalidDefinition
	}
	if int64(len(definition.Arguments)) > limitByName("stdio_arguments") || int64(len(definition.Environment)) > limitByName("stdio_environment_entries") || int64(len(definition.SecretEnvironment)) > limitByName("stdio_secret_environment_entries") {
		return ErrStdioInvalidDefinition
	}
	var argumentBytes int64
	for _, argument := range definition.Arguments {
		argumentBytes += int64(len(argument))
		if !validUTF8Value(argument) || int64(len(argument)) > limitByName("stdio_argument_bytes") {
			return ErrStdioInvalidDefinition
		}
	}
	if argumentBytes > limitByName("stdio_arguments_bytes") {
		return ErrStdioInvalidDefinition
	}
	required := make(map[string]struct{}, len(definition.SecretEnvironment))
	for environmentName, slot := range definition.SecretEnvironment {
		if !validEnvironmentName(environmentName) || int64(len(environmentName)) > limitByName("stdio_environment_name_bytes") || !stdioSecretSlotPattern.MatchString(slot) || int64(len(slot)) > limitByName("secret_slot_name_bytes") {
			return ErrStdioInvalidDefinition
		}
		if _, declared := definition.Environment[environmentName]; declared {
			return ErrStdioInvalidDefinition
		}
		required[slot] = struct{}{}
	}
	if len(required) != len(definition.Secrets) {
		return ErrStdioInvalidSecrets
	}
	for slot, value := range definition.Secrets {
		if _, ok := required[slot]; !ok || !validUTF8Value(value) {
			return ErrStdioInvalidSecrets
		}
	}
	for name, value := range definition.Environment {
		if !validEnvironmentName(name) || int64(len(name)) > limitByName("stdio_environment_name_bytes") || !validUTF8Value(value) || int64(len(value)) > limitByName("stdio_environment_value_bytes") {
			return ErrStdioInvalidDefinition
		}
	}
	return nil
}

func absoluteCleanPath(value string) bool {
	return filepath.IsAbs(value) && filepath.Clean(value) == value && validUTF8Value(value)
}

func validEnvironmentName(value string) bool {
	return value != "" && validUTF8Value(value) && !strings.ContainsRune(value, '=')
}

func validUTF8Value(value string) bool {
	return utf8.ValidString(value) && !strings.ContainsRune(value, '\x00')
}

func buildStdioEnvironment(definition StdioDefinition) []string {
	environment := make(map[string]string, len(definition.Environment)+len(definition.SecretEnvironment))
	for name, value := range definition.Environment {
		environment[name] = value
	}
	for name, slot := range definition.SecretEnvironment {
		environment[name] = definition.Secrets[slot]
	}
	names := make([]string, 0, len(environment))
	for name := range environment {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]string, 0, len(names))
	for _, name := range names {
		result = append(result, name+"="+environment[name])
	}
	return result
}

func limitByName(name string) int64 {
	limit, _ := contract.FixedLimitByName(name)
	return limit.Maximum
}
