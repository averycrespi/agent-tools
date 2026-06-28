.PHONY: build install test lint fmt tidy audit

build:
	go build -o telegram-mcp ./cmd/telegram-mcp

install:
	GOBIN=$(shell go env GOPATH)/bin go install ./cmd/telegram-mcp

test:
	go test -race ./...

lint:
	go tool golangci-lint run ./...

fmt:
	go tool goimports -w .

tidy:
	go mod tidy && go mod verify

audit: tidy fmt lint test
	go tool govulncheck ./...
