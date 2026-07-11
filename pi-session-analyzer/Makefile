.PHONY: build install test lint fmt tidy audit

build:
	go build -o pi-session-analyzer ./cmd/pi-session-analyzer

install:
	GOBIN=$(shell go env GOPATH)/bin go install ./cmd/pi-session-analyzer

test:
	go test -race ./...

lint:
	go tool golangci-lint run

fmt:
	go tool goimports -w .

tidy:
	go mod tidy && go mod verify

audit: tidy fmt lint test
	go tool govulncheck ./...
