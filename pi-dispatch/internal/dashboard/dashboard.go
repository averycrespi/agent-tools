package dashboard

import "net/http"

type Dashboard struct{}

func New() *Dashboard {
	return &Dashboard{}
}

func (d *Dashboard) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /unauthorized", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("Use the authenticated Dispatch Board URL printed by pd dashboard."))
	})
	mux.HandleFunc("GET /", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<!doctype html><title>Dispatch Board</title><h1>Dispatch Board</h1>"))
	})
	return mux
}
