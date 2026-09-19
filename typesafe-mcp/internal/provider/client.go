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
		return nil, errors.New("validation: unknown tool")
	}
	data, err := json.Marshal(arguments)
	if err != nil || len(data) > MaxRequestBytes || !BoundedJSON(data, MaxDepth) || !valid(name, arguments) {
		return nil, errors.New("validation: arguments violate schema or request byte/depth limit")
	}
	if ctx.Err() != nil {
		return nil, contextError(ctx.Err())
	}
	select {
	case c.slots <- struct{}{}:
		defer func() { <-c.slots }()
	default:
		return nil, errors.New("capacity: four upstream requests already in flight; no dispatch")
	}
	ctx, cancel := context.WithTimeout(ctx, DefaultDeadline)
	defer cancel()
	path, method := "/v1/models", http.MethodGet
	var body io.Reader
	if name == "evaluate" {
		var args object
		if json.Unmarshal(data, &args) != nil {
			return nil, errors.New("validation: invalid arguments")
		}
		if _, ok := args["model"]; !ok {
			args["model"] = "jev-latest"
		}
		data, _ = json.Marshal(args)
		if len(data) > MaxRequestBytes {
			return nil, errors.New("validation: request exceeds byte limit including default model")
		}
		arguments = args
		path, method = "/v1/systemone", http.MethodPost
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, origin+path, body)
	if err != nil {
		return nil, errors.New("validation: cannot construct request")
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
		return nil, errors.New("response_limit: provider response exceeds 512 KiB; no retry")
	}
	var value any
	if !BoundedJSON(data, MaxDepth) || json.Unmarshal(data, &value) != nil {
		return nil, errors.New("malformed_response: invalid JSON or nesting; no retry")
	}
	schema := "models_result"
	if name == "evaluate" {
		schema = "evaluate_result"
	}
	if !valid(schema, value) {
		return nil, errors.New("malformed_response: provider result violates response contract; no retry")
	}
	if name == "evaluate" && !corresponds(arguments.(object), value.(object)) {
		return nil, errors.New("malformed_response: answers do not correspond to questions; no retry")
	}
	if name == "list_models" {
		for _, v := range value.(object)["models"].([]any) {
			if _, err := time.Parse("2006-01-02", v.(object)["release_date"].(string)); err != nil {
				return nil, errors.New("malformed_response: invalid release date; no retry")
			}
		}
	}
	return value, nil
}

func contextError(err error) error {
	if errors.Is(err, context.Canceled) {
		return errors.New("canceled: request canceled; upstream effect may have occurred; no retry")
	}
	var network net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &network) && network.Timeout()) {
		return errors.New("deadline: request timed out; upstream effect may have occurred; no retry")
	}
	return errors.New("transport: exchange failed; upstream effect may have occurred; no retry")
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
	return fmt.Errorf("%s: provider HTTP %d%s; no automatic retry", category, response.StatusCode, guidance)
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
		probabilities := answer["probabilities"].(object)
		var expected object
		if question["type"] == "choice" {
			expected = question["criteria"].(object)
			if _, ok := expected[answer["choice"].(string)]; !ok {
				return false
			}
		} else {
			criteria := question["criteria"].([]any)
			expected = object{}
			for index, description := range criteria {
				expected[strconv.Itoa(index)] = description
			}
			if !reflect.DeepEqual(expected, answer["legend"]) || answer["score"].(float64) > float64(len(criteria)-1) {
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
