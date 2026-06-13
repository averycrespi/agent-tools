.PHONY: build install test lint fmt tidy audit

build:
	go build -o po ./cmd/po

install:
	GOBIN=$(shell go env GOPATH)/bin go install ./cmd/po

test:
	go test -race ./...

lint:
	go tool golangci-lint run ./...

fmt:
	go tool goimports -w .

tidy:
	go mod tidy && go mod verify

audit: tidy fmt lint test
	go tool govulncheck ./... || echo "govulncheck: review above (stdlib vulns require Go upgrade)"
