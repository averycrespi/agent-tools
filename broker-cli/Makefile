.PHONY: build install test test-integration lint fmt tidy audit

build:
	go build -o broker-cli ./cmd/broker-cli

install:
	GOBIN=$(shell go env GOPATH)/bin go install ./cmd/broker-cli

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
	go tool govulncheck ./... || echo "govulncheck: review above (stdlib vulns require Go upgrade)"
