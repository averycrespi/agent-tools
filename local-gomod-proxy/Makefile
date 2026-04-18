.PHONY: build install test test-integration test-e2e lint fmt tidy audit

build:
	go build -o local-gomod-proxy ./cmd/local-gomod-proxy

install:
	GOBIN=$(shell go env GOPATH)/bin go install ./cmd/local-gomod-proxy

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
	go tool govulncheck ./...
