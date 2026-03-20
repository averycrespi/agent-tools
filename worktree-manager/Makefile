.PHONY: build test lint fmt tidy audit

build:
	go build -o /tmp/bin/wt .

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
