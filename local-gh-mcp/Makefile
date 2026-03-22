.PHONY: build install test lint fmt tidy audit

build:
	go build -o local-gh-mcp ./cmd/local-gh-mcp

install:
	GOBIN=$(shell go env GOPATH)/bin go install ./cmd/local-gh-mcp

test:
	go test -race ./...

test-integration:
	go test -race -tags=integration ./...

lint:
	go tool golangci-lint run ./...

fmt:
	go tool goimports -w .

tidy:
	go mod tidy && go mod verify

audit: tidy fmt lint test
	go tool govulncheck ./...
