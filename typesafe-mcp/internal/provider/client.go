package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"time"
)

const DefaultDeadline = 30 * time.Second
const origin = "https://api.typesafe.ai"

// Client owns immediate admission and a private, non-replaying transport.
type Client struct {
	key   string
	http  *http.Client
	slots chan struct{}
}

func New(key string) (*Client, error) {
	if strings.TrimSpace(key) == "" || strings.ContainsAny(key, "\r\n") {
		return nil, errors.New("authentication: set a nonblank TYPESAFE_API_KEY secret")
	}
	// No ambient proxy, cookies, HTTP/2 replay, connection reuse or transparent decompression.
	transport := &http.Transport{DialContext: (&net.Dialer{Timeout: 10 * time.Second}).DialContext, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: DefaultDeadline, MaxResponseHeaderBytes: 32 << 10, DisableKeepAlives: true, DisableCompression: true}
	return newClient(key, transport), nil
}

func newClient(key string, transport http.RoundTripper) *Client {
	return &Client{key: key, http: &http.Client{Transport: transport, Timeout: DefaultDeadline, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, slots: make(chan struct{}, MaxConcurrent)}
}

// Call never logs payloads or returns provider error text. Every dispatch is one attempt.
func (c *Client) Call(ctx context.Context, name string, arguments any) (any, error) {
	if name != "evaluate" && name != "list_models" {
		return nil, safeFailure("validation: unknown tool", "validation", "admission")
	}
	data, err := json.Marshal(arguments)
	if err != nil || len(data) > MaxRequestBytes || !BoundedJSON(data, MaxDepth) || !valid(name, arguments) {
		return nil, safeFailure("validation: arguments violate schema or request byte/depth limit", "validation", "admission")
	}
	if ctx.Err() != nil {
		return nil, contextError(ctx.Err())
	}
	select {
	case c.slots <- struct{}{}:
		defer func() { <-c.slots }()
	default:
		return nil, safeFailure("capacity: four upstream requests already in flight; no dispatch", "capacity", "admission")
	}
	ctx, cancel := context.WithTimeout(ctx, DefaultDeadline)
	defer cancel()
	path, method := "/v1/models", http.MethodGet
	var body io.Reader
	if name == "evaluate" {
		var args object
		if json.Unmarshal(data, &args) != nil {
			return nil, safeFailure("validation: invalid arguments", "validation", "admission")
		}
		if _, ok := args["model"]; !ok {
			args["model"] = "jev-latest"
		}
		data, _ = json.Marshal(args)
		if len(data) > MaxRequestBytes {
			return nil, safeFailure("validation: request exceeds byte limit including default model", "validation", "admission")
		}
		arguments = args
		path, method = "/v1/systemone", http.MethodPost
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, origin+path, body)
	if err != nil {
		return nil, safeFailure("validation: cannot construct request", "validation", "admission")
	}
	// Disallow replay even if a future transport change enables connection reuse.
	req.GetBody = nil
	req.Header.Set("Authorization", "Bearer "+c.key)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	response, err := c.http.Do(req)
	if err != nil {
		return nil, contextError(err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return nil, statusError(response)
	}
	data, err = io.ReadAll(io.LimitReader(response.Body, MaxResponseBytes+1))
	if err != nil {
		return nil, contextError(err)
	}
	if len(data) > MaxResponseBytes {
		return nil, safeFailure("response_limit: provider response exceeds 512 KiB; no retry", "response_limit", "response_decode")
	}
	var value any
	if !BoundedJSON(data, MaxDepth) || json.Unmarshal(data, &value) != nil {
		return nil, safeFailure("malformed_response: invalid JSON or nesting; no retry", "json_decode", "response_decode")
	}
	schema := "models_result"
	if name == "evaluate" {
		schema = "evaluate_result"
	}
	violations := responseViolations(schema, value)
	requestArguments, _ := arguments.(object)
	violations = append(violations, semanticResponseViolations(name, requestArguments, value)...)
	if len(violations) != 0 {
		return nil, validationFailure(schema, violations)
	}
	return value, nil
}

// Semantic checks inspect only available typed fields; structural failures must
// not hide an independently observable invalid date or answer mismatch.
func semanticResponseViolations(name string, request object, value any) []ValidationViolation {
	response, ok := value.(object)
	if !ok {
		return nil
	}
	if name == "evaluate" {
		if _, ok := response["answers"].(object); ok && !corresponds(request, response) {
			return []ValidationViolation{{Code: "correspondence", Path: "$.answers", Rule: "correspondence"}}
		}
		return nil
	}
	cards, _ := response["models"].([]any)
	for _, value := range cards {
		card, _ := value.(object)
		date, ok := card["release_date"].(string)
		if !ok {
			continue
		}
		if _, err := time.Parse("2006-01-02", date); err != nil {
			return []ValidationViolation{{Code: "invalid_date", Path: "$.models.[].release_date", Rule: "date"}}
		}
	}
	return nil
}

func contextError(err error) error {
	if errors.Is(err, context.Canceled) {
		return safeFailure("canceled: request canceled; upstream effect may have occurred; no retry", "canceled", "exchange")
	}
	var network net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &network) && network.Timeout()) {
		return safeFailure("deadline: request timed out; upstream effect may have occurred; no retry", "timeout", "exchange")
	}
	return safeFailure("transport: exchange failed; upstream effect may have occurred; no retry", "transport", "exchange")
}
func statusError(response *http.Response) error {
	category := "upstream"
	switch response.StatusCode {
	case 401, 403:
		category = "authentication"
	case 400, 404, 422:
		category = "validation"
	case 429:
		category = "rate_limit"
	case 529, 503:
		category = "overload"
	default:
		if response.StatusCode >= 300 && response.StatusCode < 400 {
			category = "redirect_rejected"
		}
	}
	guidance := ""
	// Only project a parsed delta, never arbitrary header text or upstream bodies.
	if values := response.Header.Values("Retry-After"); len(values) == 1 {
		if seconds, err := strconv.ParseUint(values[0], 10, 32); err == nil {
			guidance = fmt.Sprintf("; retry-after %d seconds", seconds)
		} else if date, err := http.ParseTime(values[0]); err == nil {
			guidance = "; retry-after " + date.UTC().Format(http.TimeFormat)
		}
	}
	diagnostic := FailureDiagnostic{Version: 1, Category: category, Phase: "response_status"}
	if response.StatusCode >= 100 && response.StatusCode <= 599 {
		status := response.StatusCode
		diagnostic.HTTPStatus = &status
	}
	if values := response.Header.Values("Retry-After"); len(values) == 1 {
		seconds, err := strconv.ParseUint(values[0], 10, 32)
		if err == nil && seconds <= 86400 {
			delta := int(seconds)
			diagnostic.RetryAfterSeconds = &delta
		} else if date, err := http.ParseTime(values[0]); err == nil {
			delta := math.Ceil(time.Until(date).Seconds())
			if delta >= 0 && delta <= 86400 {
				seconds := int(delta)
				diagnostic.RetryAfterSeconds = &seconds
			}
		}
	}
	return &failure{message: fmt.Sprintf("%s: provider HTTP %d%s; no automatic retry", category, response.StatusCode, guidance), diagnostic: diagnostic}
}

func corresponds(request, response object) bool {
	questions := request["questions"].(object)
	answers := response["answers"].(object)
	if len(questions) != len(answers) {
		return false
	}
	for id, raw := range questions {
		question := raw.(object)
		answer, ok := answers[id].(object)
		if !ok || answer["type"] != question["type"] {
			return false
		}
		if question["type"] == "noul" {
			continue
		}
		probabilities, ok := answer["probabilities"].(object)
		if !ok {
			return false
		}
		var expected object
		if question["type"] == "choice" {
			expected = question["criteria"].(object)
			choice, ok := answer["choice"].(string)
			if !ok {
				return false
			}
			if _, ok := expected[choice]; !ok {
				return false
			}
		} else {
			criteria := question["criteria"].([]any)
			expected = object{}
			for index, description := range criteria {
				expected[strconv.Itoa(index)] = description
			}
			score, ok := answer["score"].(float64)
			if !ok {
				return false
			}
			if !reflect.DeepEqual(expected, answer["legend"]) || score > float64(len(criteria)-1) {
				return false
			}
		}
		if len(expected) != len(probabilities) {
			return false
		}
		sum := 0.0
		for key := range expected {
			p, ok := probabilities[key].(float64)
			if !ok {
				return false
			}
			sum += p
		}
		if math.Abs(sum-1) > 0.00001 {
			return false
		}
	}
	return true
}

// BoundedJSON rejects excess nesting and duplicate keys before semantic decoding.
func BoundedJSON(data []byte, maximum int) bool {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if !consume(decoder, 0, maximum) {
		return false
	}
	_, err := decoder.Token()
	return errors.Is(err, io.EOF)
}
func consume(decoder *json.Decoder, depth, maximum int) bool {
	token, err := decoder.Token()
	if err != nil {
		return false
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return true
	}
	if depth >= maximum {
		return false
	}
	keys := map[string]bool{}
	for decoder.More() {
		if delimiter == '{' {
			key, err := decoder.Token()
			if err != nil {
				return false
			}
			text, ok := key.(string)
			if !ok || keys[text] {
				return false
			}
			keys[text] = true
		}
		if !consume(decoder, depth+1, maximum) {
			return false
		}
	}
	closing, err := decoder.Token()
	return err == nil && ((delimiter == '{' && closing == json.Delim('}')) || (delimiter == '[' && closing == json.Delim(']')))
}
