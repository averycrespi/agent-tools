//go:build integration

package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const modelReleaseFixture = `{"models":[{"name":"jev-latest","description":"fixture current","release_date":"2026-09-10T18:38:01.391457+00:00"},{"name":"jev-preview","description":"fixture preview","release_date":"2026-09-10T18:39:06.057655+00:00"},{"name":"date-only","description":"fixture compatibility","release_date":"2026-09-15"},{"name":"lowercase","description":"fixture lowercase separators","release_date":"2026-09-10t18:38:01z"}]}`

func TestIntegrationStdioAndGatewayCatalog(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	var attempts atomic.Int32
	var failureMode atomic.Int32
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		require.Equal(t, "Bearer fixture-only", r.Header.Get("Authorization"))
		switch failureMode.Load() {
		case 1:
			w.WriteHeader(401)
			_, _ = io.WriteString(w, "SECRET-PROVIDER-BODY")
			return
		case 2:
			w.Header().Set("Retry-After", "12")
			w.Header().Set("X-Secret", "SECRET-HEADER")
			w.WriteHeader(429)
			return
		case 3:
			<-r.Context().Done()
			return
		case 4:
			_, _ = io.WriteString(w, "SECRET-INVALID-JSON")
			return
		case 5:
			_, _ = io.WriteString(w, `{"secret":"SECRET-WRONG-SHAPE"}`)
			return
		}
		switch r.URL.Path {
		case "/v1/models":
			require.Equal(t, http.MethodGet, r.Method)
			_, _ = io.WriteString(w, modelReleaseFixture)
		case "/v1/systemone":
			require.Equal(t, http.MethodPost, r.Method)
			_, _ = io.WriteString(w, `{"model":"jev-1.13.0","answers":{"q":{"type":"noul","noul":0.75}},"usage":{"input_tokens":1,"output_tokens":2}}`)
		default:
			t.Error("unexpected fixture path")
			w.WriteHeader(404)
		}
	}))
	defer fixture.Close()
	module, err := filepath.Abs("../..")
	require.NoError(t, err)
	temp := t.TempDir()
	// The real CLI is built unchanged except for the compiled destination constant.
	// No endpoint/environment override is added to the shipped executable.
	source := filepath.Join(module, "internal/provider/client.go")
	raw, err := os.ReadFile(source)
	require.NoError(t, err)
	require.Contains(t, string(raw), `const origin = "https://api.typesafe.ai"`)
	replacement := strings.Replace(string(raw), `const origin = "https://api.typesafe.ai"`, `const origin = `+strconv.Quote(fixture.URL), 1)
	replacement = strings.Replace(replacement, "const DefaultDeadline = 30 * time.Second", "const DefaultDeadline = 500 * time.Millisecond", 1)
	overlaySource := filepath.Join(temp, "client.go")
	require.NoError(t, os.WriteFile(overlaySource, []byte(replacement), 0600))
	overlay, _ := json.Marshal(map[string]any{"Replace": map[string]string{source: overlaySource}})
	overlayPath := filepath.Join(temp, "overlay.json")
	require.NoError(t, os.WriteFile(overlayPath, overlay, 0600))
	binary := filepath.Join(temp, "typesafe-mcp")
	build := exec.CommandContext(ctx, "go", "build", "-race", "-overlay", overlayPath, "-o", binary, "./cmd/typesafe-mcp")
	build.Dir = module
	output, err := build.CombinedOutput()
	require.NoError(t, err, string(output))
	cmd := exec.CommandContext(ctx, binary)
	cmd.Env = []string{"TYPESAFE_API_KEY=fixture-only"}
	cmd.WaitDelay = 2 * time.Second
	input, err := cmd.StdinPipe()
	require.NoError(t, err)
	outputPipe, err := cmd.StdoutPipe()
	require.NoError(t, err)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	require.NoError(t, cmd.Start())
	waited := false
	defer func() {
		_ = input.Close()
		if !waited {
			cancel()
			_ = cmd.Wait()
		}
	}()
	scanner := bufio.NewScanner(outputPipe)
	scanner.Buffer(make([]byte, 4096), 2<<20)
	id := 0
	call := func(method string, params any) map[string]json.RawMessage {
		t.Helper()
		id++
		message, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
		_, err := input.Write(append(message, '\n'))
		require.NoError(t, err)
		require.True(t, scanner.Scan(), "stdio response missing: %v", scanner.Err())
		var response map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(scanner.Bytes(), &response))
		require.Equal(t, strconv.Itoa(id), string(response["id"]))
		require.Equal(t, `"2.0"`, string(response["jsonrpc"]))
		return response
	}
	response := call("initialize", map[string]any{"protocolVersion": "2025-11-25", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "fixture", "version": "1"}})
	require.Nil(t, response["error"])
	_, err = io.WriteString(input, "{\"jsonrpc\":\"2.0\",\"method\":\"notifications/initialized\"}\n")
	require.NoError(t, err)
	response = call("tools/list", map[string]any{})
	require.Nil(t, response["error"])
	var listed struct {
		Tools []json.RawMessage `json:"tools"`
	}
	require.NoError(t, json.Unmarshal(response["result"], &listed))
	require.Len(t, listed.Tools, 2)
	catalogPath := filepath.Join(temp, "catalog.json")
	require.NoError(t, os.WriteFile(catalogPath, response["result"], 0600))
	for _, name := range []string{"list_models", "evaluate"} {
		args := map[string]any{}
		if name == "evaluate" {
			args = map[string]any{"state": "fixture", "questions": map[string]any{"q": map[string]any{"type": "noul", "instructions": "yes?"}}}
		}
		response = call("tools/call", map[string]any{"name": name, "arguments": args})
		require.Nil(t, response["error"])
		require.Contains(t, string(response["result"]), `"structuredContent"`)
		require.NotContains(t, string(response["result"]), `"isError":true`)
		if name == "list_models" {
			var result struct {
				StructuredContent json.RawMessage `json:"structuredContent"`
			}
			require.NoError(t, json.Unmarshal(response["result"], &result))
			require.JSONEq(t, modelReleaseFixture, string(result.StructuredContent))
		}
	}
	response = call("tools/call", map[string]any{"name": "list_models", "arguments": map[string]any{"secret": "never dispatch"}})
	require.Contains(t, string(response["result"]), `"isError":true`)
	require.EqualValues(t, 2, attempts.Load())
	var failures []json.RawMessage
	for index, category := range []string{"authentication", "rate_limit", "timeout", "json_decode", "response_contract"} {
		failureMode.Store(int32(index + 1))
		response = call("tools/call", map[string]any{"name": "list_models", "arguments": map[string]any{}})
		require.Nil(t, response["error"])
		require.Contains(t, string(response["result"]), `"isError":true`)
		require.Contains(t, string(response["result"]), `"category":"`+category+`"`)
		require.NotContains(t, string(response["result"]), "SECRET-")
		failures = append(failures, response["result"])
	}
	require.EqualValues(t, 7, attempts.Load(), "exactly one HTTP attempt for each accepted call")
	failurePath := filepath.Join(temp, "failures.json")
	encodedFailures, err := json.Marshal(failures)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(failurePath, encodedFailures, 0600))
	require.NoError(t, input.Close())
	require.False(t, scanner.Scan(), "unexpected non-protocol stdout")
	require.NoError(t, scanner.Err())
	require.NoError(t, cmd.Wait())
	waited = true
	require.Empty(t, stderr.String())
	verifyGatewayCatalog(t, ctx, module, temp, catalogPath, failurePath)
}

func verifyGatewayCatalog(t *testing.T, ctx context.Context, module, temp, catalogPath, failurePath string) {
	t.Helper()
	probe := filepath.Join(temp, "probe")
	require.NoError(t, os.Mkdir(probe, 0700))
	// A temporary lexical child may import the actual checked-out Gateway internals;
	// no product dependency, copied schema implementation, or live Gateway is involved.
	require.NoError(t, os.WriteFile(filepath.Join(probe, "go.mod"), []byte("module github.com/averycrespi/agent-tools/agent-gateway/catalogprobe\n\ngo 1.26.6\n"), 0600))
	program := `package main
import("context";"encoding/json";"os";"github.com/averycrespi/agent-tools/agent-gateway/internal/catalog";"github.com/averycrespi/agent-tools/agent-gateway/internal/downstream";"github.com/averycrespi/agent-tools/agent-gateway/internal/invocation";"github.com/averycrespi/agent-tools/agent-gateway/internal/contract")
type page []byte
func(p page) Request(context.Context,string,json.RawMessage,string)(downstream.Response,error){return downstream.Response{Result:json.RawMessage(p)},nil}
func main(){raw,e:=os.ReadFile(os.Args[1]);if e!=nil{panic(e)};candidate,e:=catalog.NewTraverser().Traverse(context.Background(),page(raw),"typesafe");if e!=nil{panic(e)};normalized:=catalog.NormalizeCandidate(candidate,catalog.NormalizeOptions{ServerID:"fixture"});if len(normalized.Tools)!=2||len(normalized.Issues)!=0{panic("catalog rejected")}; raw,e=os.ReadFile(os.Args[2]);if e!=nil{panic(e)};var results []json.RawMessage;if json.Unmarshal(raw,&results)!=nil||len(results)!=5{panic("missing failures")};for i,raw:=range results{outcome:=invocation.SanitizeCallResult(downstream.CallResult{Response:downstream.Response{Result:raw}});if outcome.ErrorCode!=contract.DownstreamFailure||outcome.Diagnostics==nil||outcome.Diagnostics.ServerReported==nil||outcome.Diagnostics.ServerReported.Category!=[]string{"authentication","rate_limit","timeout","json_decode","response_contract"}[i]{panic("diagnostic rejected")}}}
`
	require.NoError(t, os.WriteFile(filepath.Join(probe, "main.go"), []byte(program), 0600))
	workspace := filepath.Join(temp, "go.work")
	require.NoError(t, os.WriteFile(workspace, []byte("go 1.26.6\nuse (\n"+strconv.Quote(filepath.Join(module, "../agent-gateway"))+"\n"+strconv.Quote(probe)+"\n)\n"), 0600))
	command := exec.CommandContext(ctx, "go", "run", ".", catalogPath, failurePath)
	command.Dir = probe
	command.Env = append(os.Environ(), "GOWORK="+workspace)
	output, err := command.CombinedOutput()
	require.NoError(t, err, string(output))
}
