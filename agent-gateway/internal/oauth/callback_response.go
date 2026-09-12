package oauth

import "net/http"

func WriteCallbackResponse(writer http.ResponseWriter, outcome CallbackOutcome) {
	status, body := http.StatusBadRequest, callbackFailedHTML
	switch outcome {
	case CallbackSucceeded:
		status, body = http.StatusOK, callbackSucceededHTML
	case CallbackTransient:
		status, body = http.StatusServiceUnavailable, callbackTransientHTML
	}
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	writer.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
	writer.Header().Set("Referrer-Policy", "no-referrer")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	writer.WriteHeader(status)
	_, _ = writer.Write([]byte(body))
}

const (
	callbackSucceededHTML = "<!doctype html><html><head><meta charset=\"utf-8\"><title>Authorization complete</title></head><body>Authorization complete. You may close this window.</body></html>\n"
	callbackFailedHTML    = "<!doctype html><html><head><meta charset=\"utf-8\"><title>Authorization failed</title></head><body>Authorization failed. Return to Gateway and start a new authorization flow.</body></html>\n"
	callbackTransientHTML = "<!doctype html><html><head><meta charset=\"utf-8\"><title>Gateway unavailable</title></head><body>Gateway is temporarily unavailable. Retry the authorization callback.</body></html>\n"
)
