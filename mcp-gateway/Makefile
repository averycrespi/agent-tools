STRESS_COUNT ?= 20

.PHONY: build install test test-unit test-integration test-integration-s5 test-security-s5 test-stress-s5 test-e2e test-keyring-native accept-s2-1 accept-s3 accept-s4 accept-s5 lint fmt tidy verify audit

build:
	go build -o mcp-gateway ./cmd/mcp-gateway

install:
	GOBIN=$(shell go env GOPATH)/bin go install ./cmd/mcp-gateway

test:
	go test -race ./...

test-unit:
	go test -race -count=1 -timeout=90s ./...

test-integration:
	go test -race -tags=integration ./...

test-integration-s5:
	go test -race -count=1 -tags=integration -timeout=30s -run '^(TestS5Integration.*|TestConfiguredConnectionsEnforcePragmasAndFiniteBusyDeadline|TestBusyBeyondDeadlineLatchesMutationAcrossRestart|TestRepositoryRetainsNewest4096ByMonotonicSequence|TestRepositoryRollsBackEvictionWhenInsertFails|TestInFlightAllowEvictionMakesTerminalAnnotationABenignMiss)$$' ./internal/storage ./internal/backup ./internal/grantrequests ./internal/authorization ./internal/catalog ./internal/invocation ./internal/composition

test-security-s5:
	go test -race -count=1 -tags=security -timeout=30s ./test/security/...

test-stress-s5:
	go test -race -tags=stress -count=$(STRESS_COUNT) -timeout=2m -run '^(TestS5StressConcurrentSemanticDeduplication|TestS5StressApprovalCancellationPolicyLinearization)$$' ./internal/grantrequests
	go test -race -tags=stress -count=$(STRESS_COUNT) -timeout=90s -run '^(TestS5StressSyntheticSnapshotPagination|TestS5StressLostLocalMutationResponseDeduplicatesExplicitRetry)$$' ./internal/selfservice
	go test -race -tags=stress -count=$(STRESS_COUNT) -timeout=90s -run '^TestS5StressLocalInvocationDrainCleanup$$' ./internal/composition

test-e2e:
	go test -race -count=1 -tags=e2e -timeout=2m ./test/e2e/...

test-keyring-native:
	./test/keyring-native.sh

accept-s2-1:
	@go run ./test/acceptance/cmd --profile s2_1

accept-s3:
	@go run ./test/acceptance/cmd --profile s3

accept-s4:
	@go run ./test/acceptance/cmd --profile s4

accept-s5:
	@test -n "$(REPORT)" || (echo "REPORT is required" >&2; exit 2)
	@go run ./test/acceptance/cmd --profile s5 --output "$(REPORT)"

lint:
	go tool golangci-lint run ./...

fmt:
	go tool goimports -w .

tidy:
	go mod tidy && go mod verify

verify:
	go mod tidy -diff
	go mod verify
	go tool golangci-lint fmt --diff
	go tool golangci-lint run ./...

audit: tidy fmt lint test
	go tool govulncheck ./... || echo "govulncheck: review above (stdlib vulns require Go upgrade)"
