//go:build e2e

package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"strconv"
	"time"
	"unicode/utf8"
)

type object = map[string]any

const protocol = "2026-07-28"

var documents = map[string]string{"welcome": "Welcome to the local Gateway demo. No external services are used.", "permissions": "Demo Reader can echo and look up documents, but must request arithmetic access."}

func schema(properties object, required ...string) object {
	if required == nil {
		required = []string{}
	}
	return object{"type": "object", "properties": properties, "required": required, "additionalProperties": false}
}

var tools = map[string][]object{
	"workshop": {
		{"name": "echo", "description": "Echo at most 256 characters", "inputSchema": schema(object{"text": object{"type": "string", "maxLength": 256}}, "text")},
		{"name": "add", "description": "Add two bounded finite numbers", "inputSchema": schema(object{"a": object{"type": "number", "minimum": -1000000, "maximum": 1000000}, "b": object{"type": "number", "minimum": -1000000, "maximum": 1000000}}, "a", "b")},
		{"name": "controlled_error", "description": "Return a deliberate harmless tool error", "inputSchema": schema(object{})},
	},
	"library": {{"name": "lookup", "description": "Look up a fixed bundled sample document", "inputSchema": schema(object{"document": object{"type": "string", "enum": []string{"welcome", "permissions"}}}, "document")}},
}

func toolResult(kind, name string, args object) (object, error) {
	valid := false
	for _, tool := range tools[kind] {
		if tool["name"] == name {
			valid = true
		}
	}
	invalid := errors.New("invalid demo tool arguments")
	if !valid || args == nil {
		return nil, invalid
	}
	var text string
	switch name {
	case "echo":
		var ok bool
		text, ok = args["text"].(string)
		if !ok || len(args) != 1 || utf8.RuneCountInString(text) > 256 {
			return nil, invalid
		}
	case "add":
		a, aOK := args["a"].(float64)
		b, bOK := args["b"].(float64)
		if !aOK || !bOK || len(args) != 2 || math.IsNaN(a) || math.IsNaN(b) || math.Abs(a) > 1000000 || math.Abs(b) > 1000000 {
			return nil, invalid
		}
		text = strconv.FormatFloat(a+b, 'f', -1, 64)
	case "lookup":
		doc, ok := args["document"].(string)
		if !ok || len(args) != 1 {
			return nil, invalid
		}
		text, ok = documents[doc]
		if !ok {
			return nil, invalid
		}
	case "controlled_error":
		if len(args) != 0 {
			return nil, invalid
		}
		return object{"content": []object{{"type": "text", "text": "Deliberate demo tool error"}}, "isError": true}, nil
	}
	return object{"content": []object{{"type": "text", "text": text}}}, nil
}
func fixtureHandler(kind string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bad := func() { http.Error(w, "invalid demo request", http.StatusBadRequest) }
		if r.Method != "POST" || r.URL.RequestURI() != "/mcp" || r.ContentLength <= 0 || r.ContentLength > 8192 || len(r.TransferEncoding) != 0 {
			bad()
			return
		}
		raw, err := io.ReadAll(io.LimitReader(r.Body, 8193))
		if err != nil || len(raw) > 8192 {
			bad()
			return
		}
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				Name      string `json:"name"`
				Arguments object `json:"arguments"`
			} `json:"params"`
		}
		if json.Unmarshal(raw, &request) != nil || len(request.ID) == 0 {
			bad()
			return
		}
		var result object
		switch request.Method {
		case "server/discover":
			result = object{"ttlMs": 0, "cacheScope": "public", "supportedVersions": []string{protocol}, "capabilities": object{}}
		case "tools/list":
			result = object{"tools": tools[kind], "nextCursor": nil}
		case "tools/call":
			result, err = toolResult(kind, request.Params.Name, request.Params.Arguments)
			if err != nil {
				bad()
				return
			}
		default:
			bad()
			return
		}
		body, err := json.Marshal(object{"jsonrpc": "2.0", "id": request.ID, "result": result})
		if err != nil {
			bad()
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		_, _ = w.Write(body)
	})
}
func serveFixture(ctx context.Context, kind, endpoint string) error {
	if _, ok := tools[kind]; !ok {
		return errors.New("unknown fixture")
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return errors.New("fixture listen failed")
	}
	defer func() { _ = listener.Close() }()
	_, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		return err
	}
	if err = writePrivate(endpoint, []byte(port)); err != nil {
		return errors.New("fixture endpoint publication failed")
	}
	server := &http.Server{Handler: fixtureHandler(kind), ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 3 * time.Second, WriteTimeout: 3 * time.Second, IdleTimeout: 3 * time.Second, MaxHeaderBytes: 8192, ErrorLog: log.New(io.Discard, "", 0)}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	select {
	case <-ctx.Done():
		_ = server.Close()
		<-done
		return nil
	case err = <-done:
		return err
	}
}
