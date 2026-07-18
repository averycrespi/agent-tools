.PHONY: build install test integration-test lint fmt tidy audit

build:
	go build -o wts ./cmd/wts
	go build -o wtsd ./cmd/wtsd

install:
	GOBIN=$(shell go env GOPATH)/bin go install ./cmd/wts ./cmd/wtsd

test:
	go test -race ./...

integration-test:
	WTS_INTEGRATION=1 go test -race -tags=integration ./...

lint:
	go tool golangci-lint run

fmt:
	go tool goimports -w .

tidy:
	go mod tidy && go mod verify

audit: tidy fmt lint test
	go tool govulncheck ./...
