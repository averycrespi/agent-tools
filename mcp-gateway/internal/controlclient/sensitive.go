package controlclient

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	gatewaypaths "github.com/averycrespi/agent-tools/mcp-gateway/internal/paths"
)

const maxSensitiveValueBytes = 16 * 1024

var (
	ErrConfirmationRequired  = errors.New("explicit consequence confirmation is required")
	ErrSecretSinkUnavailable = errors.New("sensitive output sink is unavailable")
	ErrSecretLost            = errors.New("one-time sensitive output could not be published")
)

type ConfirmationPrompt interface {
	Confirm(string) (bool, error)
}

type ConfirmationOptions struct {
	Yes         bool
	Consequence string
	Prompt      ConfirmationPrompt
}

func RequireConfirmation(options ConfirmationOptions) error {
	if !validProblemTitle(options.Consequence) {
		return ErrConfirmationRequired
	}
	if options.Yes {
		return nil
	}
	prompt := options.Prompt
	if prompt == nil {
		prompt = terminalConfirmation{}
	}
	confirmed, err := prompt.Confirm(options.Consequence)
	if err != nil || !confirmed {
		return ErrConfirmationRequired
	}
	return nil
}

type terminalConfirmation struct{}

func (terminalConfirmation) Confirm(consequence string) (bool, error) {
	terminal, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return false, err
	}
	defer func() { _ = terminal.Close() }()
	if _, err := io.WriteString(terminal, consequence+" Type yes to continue: "); err != nil {
		return false, err
	}
	answer := make([]byte, 0, 5)
	var one [1]byte
	for len(answer) < cap(answer) {
		count, readErr := terminal.Read(one[:])
		if count == 1 {
			answer = append(answer, one[0])
			if one[0] == '\n' {
				break
			}
		}
		if readErr != nil {
			return false, readErr
		}
	}
	answer = []byte(strings.TrimSuffix(strings.TrimSuffix(string(answer), "\n"), "\r"))
	return string(answer) == "yes", nil
}

type SinkOptions struct {
	Path         string
	OpenTerminal func() (io.WriteCloser, error)
}

type sensitiveSyncWriteCloser interface {
	io.WriteCloser
	Sync() error
}

type sensitiveDirectory interface {
	Sync() error
	Close() error
}

type sensitiveSinkOps struct {
	lstat          func(string) (os.FileInfo, error)
	remove         func(string) error
	openDirectory  func(string) (sensitiveDirectory, error)
	readBearerFile func(string, int) (string, error)
	effectiveUID   func() int
}

func defaultSensitiveSinkOps() sensitiveSinkOps {
	return sensitiveSinkOps{
		lstat:  os.Lstat,
		remove: os.Remove,
		openDirectory: func(path string) (sensitiveDirectory, error) {
			return os.Open(path)
		},
		readBearerFile: readBearerFile,
		effectiveUID:   os.Geteuid,
	}
}

type PreparedSink struct {
	mu          sync.Mutex
	output      io.WriteCloser
	file        sensitiveSyncWriteCloser
	fileInfo    os.FileInfo
	path        string
	destination string
	submitted   bool
	finished    bool
	ops         sensitiveSinkOps
}

func PrepareSensitiveSink(options SinkOptions) (*PreparedSink, error) {
	if options.Path != "" && options.OpenTerminal != nil {
		return nil, ErrSecretSinkUnavailable
	}
	if options.Path != "" {
		file, err := gatewaypaths.CreateOwnerOnlyFile(options.Path)
		if err != nil {
			return nil, ErrSecretSinkUnavailable
		}
		info, err := file.Stat()
		if err != nil {
			_ = file.Close()
			return nil, ErrSecretSinkUnavailable
		}
		return &PreparedSink{output: file, file: file, fileInfo: info, path: options.Path, destination: "owner_only_file", ops: defaultSensitiveSinkOps()}, nil
	}
	opener := options.OpenTerminal
	if opener == nil {
		opener = func() (io.WriteCloser, error) {
			return os.OpenFile("/dev/tty", os.O_WRONLY, 0)
		}
	}
	terminal, err := opener()
	if err != nil || terminal == nil {
		return nil, ErrSecretSinkUnavailable
	}
	return &PreparedSink{output: terminal, destination: "controlling_terminal", ops: defaultSensitiveSinkOps()}, nil
}

func (sink *PreparedSink) Destination() string {
	if sink == nil {
		return ""
	}
	return sink.destination
}

func (sink *PreparedSink) MarkSubmitted() {
	if sink == nil {
		return
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if !sink.finished {
		sink.submitted = true
	}
}

func (sink *PreparedSink) Publish(value string) error {
	if sink == nil {
		return ErrSecretLost
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if sink.finished || !sink.submitted || sink.output == nil || !validSensitiveValue(value) {
		sink.closeUnpublished()
		return ErrSecretLost
	}
	if sink.path != "" && !sink.pathStillOwned() {
		sink.closeUnpublished()
		return ErrSecretLost
	}
	contents := value + "\n"
	count, writeErr := io.WriteString(sink.output, contents)
	if writeErr == nil && count != len(contents) {
		writeErr = io.ErrShortWrite
	}
	var syncErr error
	if writeErr == nil && sink.file != nil {
		syncErr = sink.file.Sync()
		if syncErr == nil && !sink.pathStillOwned() {
			syncErr = ErrSecretLost
		}
	}
	closeErr := sink.output.Close()
	sink.finished = true
	sink.output = nil
	if writeErr != nil || syncErr != nil || closeErr != nil {
		sink.removeOwnedPath()
		return ErrSecretLost
	}
	return nil
}

func (sink *PreparedSink) PublishAdminRotation(value string, metadata contract.AdminCredential) (string, error) {
	if sink == nil {
		return "", ErrSecretLost
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if sink.finished || !sink.submitted || sink.path == "" || sink.output == nil || sink.file == nil || !validSensitiveValue(value) || !adminBearerPattern.MatchString(value) {
		sink.closeUnpublished()
		return "", ErrSecretLost
	}
	if !sink.pathStillOwned() {
		sink.closeUnpublished()
		return "", ErrSecretLost
	}
	contents := value + "\n"
	count, writeErr := io.WriteString(sink.output, contents)
	if writeErr == nil && count != len(contents) {
		writeErr = io.ErrShortWrite
	}
	if writeErr != nil {
		sink.closeUnpublished()
		return "", ErrSecretLost
	}
	if err := sink.file.Sync(); err != nil || !sink.pathStillOwned() {
		sink.closeUnpublished()
		return "", ErrSecretLost
	}
	closeErr := sink.output.Close()
	sink.output = nil
	if closeErr != nil || !sink.pathStillOwned() {
		sink.finished = true
		sink.removeOwnedPath()
		return "", ErrSecretLost
	}
	directory, err := sink.ops.openDirectory(filepath.Dir(sink.path))
	if err != nil {
		sink.finished = true
		sink.removeOwnedPath()
		return "", ErrSecretLost
	}
	syncErr := directory.Sync()
	directoryCloseErr := directory.Close()
	if syncErr != nil || directoryCloseErr != nil || !sink.pathStillOwned() {
		sink.finished = true
		sink.removeOwnedPath()
		return "", ErrSecretLost
	}
	reopened, err := sink.ops.readBearerFile(sink.path, sink.ops.effectiveUID())
	if err != nil || reopened != value || !sink.pathStillOwned() || adminFingerprintForBearer(reopened) != metadata.Fingerprint {
		sink.finished = true
		sink.removeOwnedPath()
		return "", ErrSecretLost
	}
	sink.finished = true
	return reopened, nil
}

func (sink *PreparedSink) Cleanup() error {
	if sink == nil {
		return nil
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if sink.finished {
		return nil
	}
	sink.finished = true
	var closeErr error
	if sink.output != nil {
		closeErr = sink.output.Close()
		sink.output = nil
	}
	sink.removeOwnedPath()
	if closeErr != nil {
		return ErrSecretSinkUnavailable
	}
	return nil
}

func (sink *PreparedSink) closeUnpublished() {
	if sink.finished {
		return
	}
	sink.finished = true
	if sink.output != nil {
		_ = sink.output.Close()
		sink.output = nil
	}
	sink.removeOwnedPath()
}

func (sink *PreparedSink) pathStillOwned() bool {
	if sink.path == "" || sink.fileInfo == nil {
		return sink.path == ""
	}
	info, err := sink.ops.lstat(sink.path)
	return err == nil && info.Mode().IsRegular() && os.SameFile(sink.fileInfo, info)
}

func (sink *PreparedSink) removeOwnedPath() {
	if sink.pathStillOwned() {
		_ = sink.ops.remove(sink.path)
	}
}

func validSensitiveValue(value string) bool {
	if value == "" || len(value) > maxSensitiveValueBytes {
		return false
	}
	for _, character := range []byte(value) {
		if character < 0x20 || character > 0x7e {
			return false
		}
	}
	return true
}

func adminFingerprintForBearer(bearer string) string {
	verifier := sha256.Sum256(append([]byte("mcp-gateway/admin-verifier/v1\x00"), bearer...))
	digest := sha256.Sum256(append([]byte("mcp-gateway/admin-fingerprint/v1\x00"), verifier[:]...))
	return hex.EncodeToString(digest[:8])
}

func NewSecretSinkError(title string) *OnlineError {
	if !validProblemTitle(title) {
		title = "The one-time value could not be published."
	}
	return &OnlineError{Code: "client_secret_sink_unavailable", Title: title, Exit: 2}
}
