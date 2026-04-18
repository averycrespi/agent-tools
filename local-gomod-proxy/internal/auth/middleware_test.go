package auth

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func newReq(auth string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/foo/@v/list", nil)
	if auth != "" {
		r.Header.Set("Authorization", auth)
	}
	return r
}

func TestMiddleware_BearerValid(t *testing.T) {
	h := Middleware("secret", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	w := httptest.NewRecorder()
	h.ServeHTTP(w, newReq("Bearer secret"))
	assert.Equal(t, http.StatusNoContent, w.Code)
}

func TestMiddleware_BasicValid(t *testing.T) {
	h := Middleware("secret", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	basic := "Basic " + base64.StdEncoding.EncodeToString([]byte("_:secret"))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, newReq(basic))
	assert.Equal(t, http.StatusNoContent, w.Code)
}

func TestMiddleware_Missing(t *testing.T) {
	h := Middleware("secret", http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Fatal("handler should not be called")
	}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, newReq(""))
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestMiddleware_WrongToken(t *testing.T) {
	h := Middleware("secret", http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Fatal("handler should not be called")
	}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, newReq("Bearer wrong"))
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}
