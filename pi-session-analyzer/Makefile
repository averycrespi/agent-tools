.PHONY: build install test test-frontend lint fmt tidy audit

build:
	go build -o pi-session-analyzer ./cmd/pi-session-analyzer

install:
	GOBIN=$(shell go env GOPATH)/bin go install ./cmd/pi-session-analyzer

test:
	go test -race ./...
	$(MAKE) test-frontend

test-frontend:
	node --test internal/dashboard/frontend_test/*.test.mjs

lint:
	go tool golangci-lint run

fmt:
	go tool goimports -w .

tidy:
	go mod tidy && go mod verify

audit: tidy fmt lint test
	go tool govulncheck ./...
