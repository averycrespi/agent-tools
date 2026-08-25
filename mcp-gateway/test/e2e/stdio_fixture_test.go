//go:build e2e

package e2e

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

const stdioFixtureEnvironment = "MCP_GATEWAY_E2E_STDIO_FIXTURE"

type stdioFixtureEvent struct {
	Kind       string `json:"kind"`
	PID        int    `json:"pid"`
	Mode       string `json:"mode,omitempty"`
	Method     string `json:"method,omitempty"`
	Cursor     string `json:"cursor,omitempty"`
	PriorPID   int    `json:"prior_pid,omitempty"`
	PriorAlive bool   `json:"prior_alive,omitempty"`
}

func TestE2EStdioFixtureProcess(t *testing.T) {
	if os.Getenv(stdioFixtureEnvironment) != "1" {
		return
	}
	arguments := fixtureArguments(os.Args)
	if len(arguments) != 4 || arguments[0] != "mcp" {
		os.Exit(90)
	}
	mode, markerPath, eventsPath := arguments[1], arguments[2], arguments[3]
	pid := os.Getpid()
	priorPID, priorAlive := 0, false
	fallbackProbe := mode == "legacy"
	gatedReplacement := false
	repeatFault := false
	if strings.HasPrefix(mode, "gated-modern") {
		contents, err := os.ReadFile(markerPath)
		if err == nil {
			priorPID, _ = strconv.Atoi(string(contents))
			priorAlive = processExists(priorPID)
			gatedReplacement = true
		}
		_ = os.WriteFile(markerPath, []byte(strconv.Itoa(pid)), 0o600)
	}
	if mode == "process-failure" || mode == "output-failure" || mode == "protocol-failure" {
		contents, err := os.ReadFile(markerPath)
		if err == nil {
			priorPID, _ = strconv.Atoi(string(contents))
			repeatFault = true
		} else {
			_ = os.WriteFile(markerPath, []byte(strconv.Itoa(pid)), 0o600)
		}
	}
	if mode == "auto" {
		contents, err := os.ReadFile(markerPath)
		if err != nil {
			fallbackProbe = true
			_ = os.WriteFile(markerPath, []byte(strconv.Itoa(pid)), 0o600)
		} else {
			priorPID, _ = strconv.Atoi(string(contents))
			priorAlive = processExists(priorPID)
		}
	}
	appendFixtureEvent(eventsPath, stdioFixtureEvent{Kind: "start", PID: pid, Mode: mode, PriorPID: priorPID, PriorAlive: priorAlive})
	if gatedReplacement {
		release := make(chan os.Signal, 1)
		signal.Notify(release, syscall.SIGUSR1)
		appendFixtureEvent(eventsPath, stdioFixtureEvent{Kind: "blocked", PID: pid})
		<-release
		signal.Stop(release)
		appendFixtureEvent(eventsPath, stdioFixtureEvent{Kind: "released", PID: pid})
	}
	if repeatFault {
		if mode == "process-failure" {
			os.Exit(42)
		}
		if mode == "protocol-failure" {
			_, _ = fmt.Fprintln(os.Stdout, `{`)
		} else {
			_, _ = fmt.Fprintln(os.Stdout, strings.Repeat("x", 10*1024*1024))
		}
		select {}
	}

	frames := make(chan []byte)
	go func() {
		scanner := bufio.NewScanner(os.Stdin)
		scanner.Buffer(make([]byte, 4096), 256*1024)
		for scanner.Scan() {
			frames <- append([]byte(nil), scanner.Bytes()...)
		}
		close(frames)
	}()
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM, os.Interrupt)
	defer signal.Stop(signals)
	for {
		select {
		case received := <-signals:
			appendFixtureEvent(eventsPath, stdioFixtureEvent{Kind: "signal", PID: pid, Mode: received.String()})
			if mode != "blocked-stop" {
				return
			}
		case frame, ok := <-frames:
			if !ok {
				appendFixtureEvent(eventsPath, stdioFixtureEvent{Kind: "eof", PID: pid})
				if mode != "blocked-stop" {
					return
				}
				frames = nil
				continue
			}
			handleFixtureFrame(mode, fallbackProbe, eventsPath, pid, frame)
		}
	}
}

func handleFixtureFrame(mode string, fallbackProbe bool, eventsPath string, pid int, frame []byte) {
	var request struct {
		ID     uint64 `json:"id"`
		Method string `json:"method"`
		Params struct {
			Cursor string `json:"cursor"`
		} `json:"params"`
	}
	if json.Unmarshal(frame, &request) != nil {
		os.Exit(91)
	}
	appendFixtureEvent(eventsPath, stdioFixtureEvent{Kind: "request", PID: pid, Method: request.Method, Cursor: request.Params.Cursor})
	switch request.Method {
	case "server/discover":
		if fallbackProbe {
			_, _ = fmt.Fprintf(os.Stdout, `{"jsonrpc":"2.0","id":%d,"error":{"code":-32601,"message":"Method not found"}}`+"\n", request.ID)
			return
		}
		_, _ = fmt.Fprintf(os.Stdout, `{"jsonrpc":"2.0","id":%d,"result":{"ttlMs":0,"cacheScope":"public","supportedVersions":["2026-07-28"],"capabilities":{}}}`+"\n", request.ID)
	case "initialize":
		_, _ = fmt.Fprintf(os.Stdout, `{"jsonrpc":"2.0","id":%d,"result":{"protocolVersion":"2025-11-25","capabilities":{},"serverInfo":{"name":"e2e-fixture","version":"1"}}}`+"\n", request.ID)
	case "tools/list":
		if request.Params.Cursor == "" {
			_, _ = fmt.Fprintf(os.Stdout, `{"jsonrpc":"2.0","id":%d,"result":{"tools":[{"name":"alpha","description":"first fixture page","inputSchema":{"type":"object"}}],"nextCursor":"page-2"}}`+"\n", request.ID)
			return
		}
		_, _ = fmt.Fprintf(os.Stdout, `{"jsonrpc":"2.0","id":%d,"result":{"tools":[{"name":"beta","description":"second fixture page","inputSchema":{"type":"object"}}],"nextCursor":null}}`+"\n", request.ID)
		if mode == "output-failure" {
			_, _ = fmt.Fprintln(os.Stdout, strings.Repeat("x", 10*1024*1024))
		} else if mode == "protocol-failure" {
			_, _ = fmt.Fprintln(os.Stdout, `{`)
		}
	}
}

func fixtureArguments(arguments []string) []string {
	for index, argument := range arguments {
		if argument == "--" && index+1 < len(arguments) {
			return arguments[index+1:]
		}
	}
	return nil
}

func appendFixtureEvent(path string, event stdioFixtureEvent) {
	contents, err := json.Marshal(event)
	if err != nil {
		os.Exit(92)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		os.Exit(93)
	}
	contents = append(contents, '\n')
	_, writeErr := file.Write(contents)
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		os.Exit(94)
	}
}

func processExists(pid int) bool {
	return pid > 0 && syscall.Kill(pid, 0) == nil
}
