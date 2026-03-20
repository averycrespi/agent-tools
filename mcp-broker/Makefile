.PHONY: build test test-integration lint fmt tidy audit

build:
	go build -o mcp-broker ./cmd/mcp-broker

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
