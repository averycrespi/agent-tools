package provider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const mixed = `{"state":{"messages":["hello"]},"questions":{"route":{"type":"choice","instructions":{"task":"route"},"criteria":{"a":{"nested":[true,2]},"b":null}},"quality":{"type":"score","instructions":["rate"],"criteria":[null,{"rubric":["good"]}]},"urgent":{"type":"noul","instructions":"urgent?","criteria":{"true":[],"false":null}}}}`
const result = `{"model":"jev-1.13.0","answers":{"route":{"type":"choice","choice":"a","confidence":0.8,"probabilities":{"a":0.9,"b":0.1}},"quality":{"type":"score","score":0.7,"confidence":0.4,"legend":{"0":null,"1":{"rubric":["good"]}},"probabilities":{"0":0.3,"1":0.7}},"urgent":{"type":"noul","noul":0.6}},"usage":{"input_tokens":100,"output_tokens":20}}`
const models = `{"models":[{"name":"jev-latest","description":"current","release_date":"2026-09-15"}]}`

func decode(t *testing.T, s string) object {
	t.Helper()
	var v object
	require.NoError(t, json.Unmarshal([]byte(s), &v))
	return v
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func fixture(t *testing.T, handler http.HandlerFunc) (*Client, *atomic.Int32) {
	t.Helper()
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { attempts.Add(1); handler(w, r) }))
	t.Cleanup(srv.Close)
	base := srv.Client().Transport
	client := newClient("fixture-secret", roundTrip(func(r *http.Request) (*http.Response, error) {
		require.Equal(t, "api.typesafe.ai", r.URL.Host)
		require.Equal(t, "https", r.URL.Scheme)
		require.Equal(t, "Bearer fixture-secret", r.Header.Get("Authorization"))
		copy := r.Clone(r.Context())
		copy.URL.Scheme = "http"
		copy.URL.Host = strings.TrimPrefix(srv.URL, "http://")
		return base.RoundTrip(copy)
	}))
	return client, &attempts
}
func TestEvaluateMixedAndPinning(t *testing.T) {
	for _, model := range []string{"", "jev-9.99.2"} {
		t.Run(model, func(t *testing.T) {
			args := decode(t, mixed)
			expected := "jev-latest"
			if model != "" {
				args["model"] = model
				expected = model
			}
			client, count := fixture(t, func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, "POST", r.Method)
				require.Equal(t, "/v1/systemone", r.URL.Path)
				var sent object
				require.NoError(t, json.NewDecoder(r.Body).Decode(&sent))
				require.Equal(t, expected, sent["model"])
				require.Equal(t, args["state"], sent["state"])
				require.Equal(t, args["questions"], sent["questions"])
				_, _ = io.WriteString(w, result)
			})
			value, err := client.Call(t.Context(), "evaluate", args)
			require.NoError(t, err)
			require.Equal(t, decode(t, result), value)
			require.EqualValues(t, 1, count.Load())
		})
	}
}
func TestModels(t *testing.T) {
	client, count := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "GET", r.Method)
		require.Equal(t, "/v1/models", r.URL.Path)
		_, _ = io.WriteString(w, models)
	})
	value, err := client.Call(t.Context(), "list_models", object{})
	require.NoError(t, err)
	require.Equal(t, decode(t, models), value)
	require.EqualValues(t, 1, count.Load())
}
func TestInvalidInputsNeverDispatch(t *testing.T) {
	inputs := []string{`{}`, `{"state":null,"questions":{}}`, strings.Replace(mixed, `"choice"`, `"other"`, 1), strings.Replace(mixed, `"state":`, `"url":"https://evil.test","state":`, 1), strings.Replace(mixed, `"instructions":"urgent?"`, `"instructions":null`, 1), strings.Replace(mixed, `"true":[]`, `"extra":[]`, 1), strings.Replace(mixed, `[null,{"rubric":["good"]}]`, `[null]`, 1), strings.Replace(mixed, `"b":null`, `"b":false`, 1), strings.Replace(mixed, `"state":`, `"model":" ","state":`, 1), strings.Replace(mixed, `"type":"noul"`, `"type":"noul","headers":{}`, 1)}
	client, count := fixture(t, func(http.ResponseWriter, *http.Request) { t.Error("unexpected dispatch") })
	for _, s := range inputs {
		_, err := client.Call(t.Context(), "evaluate", decode(t, s))
		require.Error(t, err)
	}
	for _, args := range []object{{"x": 1}, {"credentials": "secret"}} {
		_, err := client.Call(t.Context(), "list_models", args)
		require.Error(t, err)
	}
	args := decode(t, mixed)
	args["state"] = strings.Repeat("x", MaxRequestBytes)
	_, err := client.Call(t.Context(), "evaluate", args)
	require.Error(t, err)
	args = decode(t, mixed)
	questions := object{}
	for i := 0; i <= MaxQuestions; i++ {
		questions[strings.Repeat("x", i)] = args["questions"].(object)["urgent"]
	}
	args["questions"] = questions
	_, err = client.Call(t.Context(), "evaluate", args)
	require.Error(t, err)
	args = decode(t, mixed)
	var nested any = "x"
	for i := 0; i < MaxDepth; i++ {
		nested = []any{nested}
	}
	args["state"] = nested
	_, err = client.Call(t.Context(), "evaluate", args)
	require.Error(t, err)
	require.Zero(t, count.Load())
}
func TestMalformedResponses(t *testing.T) {
	invalid := []string{`{}`, `not json`, result + `{}`, strings.Replace(result, `"model":"jev-1.13.0",`, "", 1), strings.Replace(result, `"urgent":`, `"wrong":`, 1), strings.Replace(result, `"type":"noul"`, `"type":"choice"`, 1), strings.Replace(result, `"noul":0.6`, `"noul":1.6`, 1), strings.Replace(result, `"choice":"a"`, `"choice":"c"`, 1), strings.Replace(result, `"a":0.9`, `"a":0.7`, 1), strings.Replace(result, `"score":0.7`, `"score":2`, 1), strings.Replace(result, `"0":null`, `"0":"wrong"`, 1), strings.Replace(result, `"output_tokens":20`, `"output_tokens":null`, 1), strings.Replace(result, `"noul":0.6`, `"noul":0.6,"noul":0.5`, 1)}
	for _, body := range invalid {
		t.Run(body[:min(30, len(body))], func(t *testing.T) {
			client, count := fixture(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, body) })
			_, err := client.Call(t.Context(), "evaluate", decode(t, mixed))
			require.ErrorContains(t, err, "malformed_response")
			require.EqualValues(t, 1, count.Load())
		})
	}
}
func TestSafeOneShotErrors(t *testing.T) {
	for status, category := range map[int]string{401: "authentication", 403: "authentication", 422: "validation", 429: "rate_limit", 529: "overload", 503: "overload", 500: "upstream", 302: "redirect_rejected"} {
		t.Run(category, func(t *testing.T) {
			client, count := fixture(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Retry-After", "7")
				w.Header().Set("Location", "https://evil.test/fixture-secret")
				w.WriteHeader(status)
				_, _ = io.WriteString(w, "fixture-secret private body")
			})
			_, err := client.Call(t.Context(), "evaluate", decode(t, mixed))
			require.ErrorContains(t, err, category)
			require.ErrorContains(t, err, "retry-after 7 seconds")
			require.NotContains(t, err.Error(), "fixture-secret")
			require.NotContains(t, err.Error(), "private body")
			require.EqualValues(t, 1, count.Load())
		})
	}
	client := newClient("fixture-secret", roundTrip(func(*http.Request) (*http.Response, error) { return nil, errors.New("fixture-secret private state") }))
	_, err := client.Call(t.Context(), "evaluate", decode(t, mixed))
	require.ErrorContains(t, err, "transport")
	require.NotContains(t, err.Error(), "fixture-secret")
}
func TestResponseLimitsAndCancellation(t *testing.T) {
	client, count := fixture(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, strings.Repeat(" ", MaxResponseBytes+1))
	})
	_, err := client.Call(t.Context(), "list_models", object{})
	require.ErrorContains(t, err, "response_limit")
	require.EqualValues(t, 1, count.Load())
	for _, cancelled := range []bool{false, true} {
		entered := make(chan struct{})
		client, count = fixture(t, func(_ http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(io.Discard, r.Body)
			close(entered)
			select {
			case <-r.Context().Done():
			case <-time.After(time.Second):
				t.Error("fixture did not observe cancellation")
			}
		})
		ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
		if cancelled {
			go func() { <-entered; cancel() }()
		}
		_, err = client.Call(ctx, "evaluate", decode(t, mixed))
		cancel()
		category := "deadline"
		if cancelled {
			category = "canceled"
		}
		require.ErrorContains(t, err, category)
		require.EqualValues(t, 1, count.Load())
	}
}
func TestCapacityAndPredispatchCancellation(t *testing.T) {
	client, count := fixture(t, func(http.ResponseWriter, *http.Request) { t.Error("unexpected dispatch") })
	for i := 0; i < MaxConcurrent; i++ {
		client.slots <- struct{}{}
	}
	_, err := client.Call(t.Context(), "list_models", object{})
	require.ErrorContains(t, err, "capacity")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = client.Call(ctx, "list_models", object{})
	require.ErrorContains(t, err, "canceled")
	require.Zero(t, count.Load())
}
func TestCredentialsAndProductionTransport(t *testing.T) {
	for _, key := range []string{"", " \t\n", "secret\r\n"} {
		_, err := New(key)
		require.ErrorContains(t, err, "TYPESAFE_API_KEY")
		require.NotContains(t, err.Error(), "secret\r\n")
	}
	client, err := New("key")
	require.NoError(t, err)
	require.Positive(t, client.http.Timeout)
	transport := client.http.Transport.(*http.Transport)
	require.True(t, transport.DisableKeepAlives)
	require.True(t, transport.DisableCompression)
	require.Nil(t, transport.Proxy)
	require.False(t, transport.ForceAttemptHTTP2)
}
func TestJSONBounds(t *testing.T) {
	for _, s := range []string{`{"x":1,"x":2}`, `{} {}`, `[`, strings.Repeat("[", MaxDepth+1) + "0" + strings.Repeat("]", MaxDepth+1)} {
		require.False(t, BoundedJSON([]byte(s), MaxDepth))
	}
	require.True(t, BoundedJSON([]byte(mixed), MaxDepth))
}
