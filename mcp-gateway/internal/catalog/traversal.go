// Package catalog owns bounded raw downstream catalog traversal.
package catalog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"
	"unicode/utf8"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/downstream"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/strictjson"
)

var (
	ErrTraversalLimit = errors.New("catalog traversal limit is reached")
	ErrInvalidPage    = errors.New("catalog page is invalid")
	ErrCursorCycle    = errors.New("catalog cursor cycle is invalid")
	ErrNameCollision  = errors.New("catalog tool name collision is invalid")
	ErrUnavailable    = errors.New("catalog traversal is unavailable")
)

type requestFailure struct{ err error }

func (failure *requestFailure) Error() string { return failure.err.Error() }
func (failure *requestFailure) Unwrap() error { return failure.err }

type PageClient interface {
	Request(context.Context, string, json.RawMessage, string) (downstream.Response, error)
}

type DeadlineFunc func(context.Context, time.Duration) (context.Context, context.CancelFunc)

type RawTool struct {
	UpstreamName string
	ExternalName string
	Descriptor   json.RawMessage
}

type IssueClass uint8

const IssueDescriptorInvalid IssueClass = 1

type Candidate struct {
	Tools    []RawTool
	Issues   []IssueClass
	RawCount int64
	Pages    int64
	Bytes    int64
}

type Traverser struct {
	admission chan struct{}
	deadline  DeadlineFunc
}

func NewTraverser() *Traverser {
	return NewTraverserWithDeadline(context.WithTimeout)
}

func NewTraverserWithDeadline(deadline DeadlineFunc) *Traverser {
	limit := fixedLimit("catalog_traversals")
	return &Traverser{admission: make(chan struct{}, int(limit)), deadline: deadline}
}

func (traverser *Traverser) Traverse(ctx context.Context, client PageClient, namespace string) (Candidate, error) {
	if traverser == nil || traverser.deadline == nil || client == nil || !validNamespace(namespace) {
		return Candidate{}, ErrInvalidPage
	}
	select {
	case traverser.admission <- struct{}{}:
		defer func() { <-traverser.admission }()
	default:
		return Candidate{}, ErrTraversalLimit
	}
	traversalCtx, cancel := traverser.deadline(ctx, contract.CatalogTraversalDeadline)
	defer cancel()
	return traverser.traverse(traversalCtx, client, namespace)
}

func (traverser *Traverser) Status() contract.LimitStatus {
	if traverser == nil {
		return contract.LimitStatus{}
	}
	inUse := int64(len(traverser.admission))
	limit := int64(cap(traverser.admission))
	return contract.LimitStatus{InUse: inUse, Limit: limit, Saturated: inUse >= limit}
}

func (traverser *Traverser) traverse(ctx context.Context, client PageClient, namespace string) (Candidate, error) {
	maximumPages := fixedLimit("tools_list_pages")
	maximumTools := fixedLimit("active_tools_per_server")
	seenCursors := make(map[string]struct{})
	seenNames := make(map[string]struct{})
	candidate := Candidate{Tools: make([]RawTool, 0), Issues: make([]IssueClass, 0)}
	cursor := ""
	for pageIndex := int64(0); pageIndex < maximumPages; pageIndex++ {
		params, _ := json.Marshal(struct {
			Cursor string `json:"cursor"`
		}{Cursor: cursor})
		pageCtx, cancel := traverser.deadline(ctx, contract.CatalogPageDeadline)
		response, err := client.Request(pageCtx, "tools/list", params, "")
		cancel()
		if err != nil {
			return Candidate{}, errors.Join(ErrUnavailable, &requestFailure{err: err})
		}
		if response.Error != nil {
			return Candidate{}, ErrUnavailable
		}
		page, err := decodePage(response.Result)
		if err != nil {
			return Candidate{}, err
		}
		candidate.Pages++
		candidate.Bytes += int64(len(response.Result))
		candidate.RawCount += int64(len(page.tools))
		for _, raw := range page.tools {
			tool, issue := isolateTool(raw, namespace)
			if issue != 0 {
				candidate.Issues = append(candidate.Issues, issue)
				continue
			}
			if _, duplicate := seenNames[tool.UpstreamName]; duplicate {
				return Candidate{}, ErrNameCollision
			}
			seenNames[tool.UpstreamName] = struct{}{}
			if int64(len(candidate.Tools)) >= maximumTools {
				return Candidate{}, ErrTraversalLimit
			}
			candidate.Tools = append(candidate.Tools, tool)
		}
		if page.nextCursor == "" {
			return candidate, nil
		}
		if pageIndex == maximumPages-1 {
			return Candidate{}, ErrTraversalLimit
		}
		if _, duplicate := seenCursors[page.nextCursor]; duplicate {
			return Candidate{}, ErrCursorCycle
		}
		seenCursors[page.nextCursor] = struct{}{}
		cursor = page.nextCursor
	}
	return Candidate{}, ErrTraversalLimit
}

type rawPage struct {
	tools      []json.RawMessage
	nextCursor string
}

func decodePage(contents []byte) (rawPage, error) {
	if !utf8.Valid(contents) || int64(len(contents)) > fixedLimit("tools_list_page_bytes") {
		return rawPage{}, ErrInvalidPage
	}
	decoder := json.NewDecoder(bytes.NewReader(contents))
	first, err := decoder.Token()
	if err != nil || first != json.Delim('{') {
		return rawPage{}, ErrInvalidPage
	}
	members := make(map[string]json.RawMessage)
	for decoder.More() {
		keyToken, keyErr := decoder.Token()
		key, ok := keyToken.(string)
		if keyErr != nil || !ok {
			return rawPage{}, ErrInvalidPage
		}
		if _, duplicate := members[key]; duplicate {
			return rawPage{}, ErrInvalidPage
		}
		var raw json.RawMessage
		if decoder.Decode(&raw) != nil {
			return rawPage{}, ErrInvalidPage
		}
		members[key] = raw
	}
	if closing, closeErr := decoder.Token(); closeErr != nil || closing != json.Delim('}') {
		return rawPage{}, ErrInvalidPage
	}
	if _, trailingErr := decoder.Token(); !errors.Is(trailingErr, io.EOF) {
		return rawPage{}, ErrInvalidPage
	}
	toolsRaw, exists := members["tools"]
	if !exists || bytes.Equal(bytes.TrimSpace(toolsRaw), []byte("null")) {
		return rawPage{}, ErrInvalidPage
	}
	var tools []json.RawMessage
	if json.Unmarshal(toolsRaw, &tools) != nil || tools == nil {
		return rawPage{}, ErrInvalidPage
	}
	nextCursor := ""
	if cursorRaw, present := members["nextCursor"]; present && !bytes.Equal(bytes.TrimSpace(cursorRaw), []byte("null")) {
		if json.Unmarshal(cursorRaw, &nextCursor) != nil {
			return rawPage{}, ErrInvalidPage
		}
	}
	return rawPage{tools: tools, nextCursor: nextCursor}, nil
}

func isolateTool(raw json.RawMessage, namespace string) (RawTool, IssueClass) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || int64(len(raw)) > fixedLimit("tool_descriptor_bytes") {
		return RawTool{}, IssueDescriptorInvalid
	}
	var object map[string]json.RawMessage
	if strictjson.Decode(raw, &object, strictjson.Options{MaxBytes: fixedLimit("tool_descriptor_bytes"), MaxDepth: 64}) != nil || object == nil {
		return RawTool{}, IssueDescriptorInvalid
	}
	var name string
	if nameRaw, exists := object["name"]; !exists || json.Unmarshal(nameRaw, &name) != nil || !validToolName(name) {
		return RawTool{}, IssueDescriptorInvalid
	}
	external := namespace + "." + name
	if int64(len(external)) > fixedLimit("external_tool_name_bytes") {
		return RawTool{}, IssueDescriptorInvalid
	}
	return RawTool{UpstreamName: name, ExternalName: external, Descriptor: append(json.RawMessage(nil), raw...)}, 0
}

func validToolName(value string) bool {
	if len(value) == 0 || int64(len(value)) > fixedLimit("tool_name_bytes") {
		return false
	}
	for _, character := range []byte(value) {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '_' || character == '-' || character == '.' {
			continue
		}
		return false
	}
	return true
}

func validNamespace(value string) bool {
	if len(value) == 0 || len(value) > 32 {
		return false
	}
	for index, character := range []byte(value) {
		if character >= 'a' && character <= 'z' || index > 0 && character >= '0' && character <= '9' || index > 0 && (character == '_' || character == '-') {
			continue
		}
		return false
	}
	return value != "mcp_gateway"
}

func fixedLimit(name string) int64 {
	value, ok := contract.FixedLimitByName(name)
	if !ok {
		panic(fmt.Sprintf("missing catalog limit %s", name))
	}
	return value.Maximum
}
