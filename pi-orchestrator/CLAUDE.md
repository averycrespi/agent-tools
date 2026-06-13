# pi-orchestrator

Go CLI tool (`po`) for running typed, durable workflow definitions above `pi-dispatcher` task runs.

## Development

```bash
make build    # go build -o po ./cmd/po
make install  # go install ./cmd/po
make test     # go test -race ./...
make lint     # go tool golangci-lint run ./...
make fmt      # go tool goimports -w .
make tidy     # go mod tidy && go mod verify
make audit    # tidy + fmt + lint + test + govulncheck
```

Run `make audit` before committing.

## Architecture

```
cmd/po/             Cobra entry point and thin command wrappers
internal/workflow/  Workflow definition loading and validation
```

Commands should delegate to internal packages. External commands must go through runner interfaces for tests.
