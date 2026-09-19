.PHONY: build install test test-integration lint fmt tidy audit

build:
	go build -o typesafe-mcp ./cmd/typesafe-mcp

install:
	GOBIN=$(shell go env GOPATH)/bin go install ./cmd/typesafe-mcp

test:
	go test -race ./...

test-integration:
	go test -race -tags=integration -run '^TestIntegration' ./internal/server

lint:
	go tool golangci-lint run ./...

fmt:
	go tool goimports -w .

tidy:
	go mod tidy && go mod verify

audit: tidy fmt lint test
	go tool govulncheck ./...
