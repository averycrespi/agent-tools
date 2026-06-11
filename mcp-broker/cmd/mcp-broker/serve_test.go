package main

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLimitRequestBodyRejectsOversizedBody(t *testing.T) {
	h := limitRequestBody(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := io.ReadAll(r.Body)
		if err != nil {
			var maxErr *http.MaxBytesError
			require.True(t, errors.As(err, &maxErr), "got %T: %v", err, err)
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		w.WriteHeader(http.StatusOK)
	}), 4)

	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader("12345"))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	require.Equal(t, http.StatusRequestEntityTooLarge, w.Code)
	require.Contains(t, w.Body.String(), "request body too large")
}

func TestLimitRequestBodyCanBeDisabled(t *testing.T) {
	h := limitRequestBody(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		w.WriteHeader(http.StatusOK)
	}), 0)

	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader("12345"))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
}

func TestLimitRequestBodyRejectsOversizedBodyWhenHandlerDoesNotHandleReadError(t *testing.T) {
	h := limitRequestBody(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}), 4)

	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader("12345"))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	require.Equal(t, http.StatusRequestEntityTooLarge, w.Code)
	require.Contains(t, w.Body.String(), "request body too large")
}
