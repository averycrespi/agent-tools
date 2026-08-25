.PHONY: build install test test-integration test-e2e test-keyring-native accept-s2-1 lint fmt tidy audit

build:
	go build -o mcp-gateway ./cmd/mcp-gateway

install:
	GOBIN=$(shell go env GOPATH)/bin go install ./cmd/mcp-gateway

test:
	go test -race ./...

test-integration:
	go test -race -tags=integration ./...

test-e2e:
	go test -race -tags=e2e -timeout=60s ./test/e2e/...

test-keyring-native:
	./test/keyring-native.sh

accept-s2-1:
	@go run ./test/acceptance/cmd

lint:
	go tool golangci-lint run ./...

fmt:
	go tool goimports -w .

tidy:
	go mod tidy && go mod verify

audit: tidy fmt lint test
	go tool govulncheck ./... || echo "govulncheck: review above (stdlib vulns require Go upgrade)"
