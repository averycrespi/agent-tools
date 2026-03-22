.PHONY: build install test test-integration test-e2e lint fmt tidy audit

build:
	go build -o mcp-broker ./cmd/mcp-broker

install:
	GOBIN=$(shell go env GOPATH)/bin go install ./cmd/mcp-broker

test:
	go test -race ./...

test-integration:
	go test -race -tags=integration ./...

test-e2e:
	go test -race -tags=e2e -timeout=60s ./test/e2e/...

lint:
	go tool golangci-lint run ./...

fmt:
	go tool goimports -w .

tidy:
	go mod tidy && go mod verify

audit: tidy fmt lint test
	go tool govulncheck ./... || echo "govulncheck: review above (stdlib vulns require Go upgrade)"
