STRESS_COUNT ?= 20

.PHONY: build install test test-unit test-integration test-integration-s5 test-security-s5 test-stress-s5 test-e2e test-keyring-native test-task-s6 test-milestone-s6 test-frontend-s6 test-unit-s6 test-integration-s6 test-browser-s6-workflows test-browser-s6-visual test-browser-s6-a11y test-browser-s6-cross test-cli-e2e-s6 test-security-s6 qualify-external-s6 accept-s2-1 accept-s3 accept-s4 accept-s5 accept-s6 lint fmt tidy verify audit

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

test-task-s6:
	@test -n "$(TASK)" || (echo "TASK is required" >&2; exit 2)
	@go run ./test/acceptance/cmd --task "$(TASK)"

test-milestone-s6:
	@test -n "$(MILESTONE)" || (echo "MILESTONE is required" >&2; exit 2)
	@go run ./test/acceptance/cmd --milestone "$(MILESTONE)"

test-frontend-s6:
	npm --prefix .. run ui:typecheck
	npm --prefix .. run ui:verify-generated
	npm --prefix .. run ui:verify-supply-chain
	go test -race -count=1 -timeout=30s -run '^TestS6StaticSupplyChain$$' ./internal/api

test-unit-s6:
	go test -race -count=1 -timeout=110s ./...

test-integration-s6:
	go test -race -count=1 -tags=integration -timeout=60s -run '^(TestS6InvocationRepositoryReads|TestS5Integration.*|TestConfiguredConnectionsEnforcePragmasAndFiniteBusyDeadline|TestBusyBeyondDeadlineLatchesMutationAcrossRestart|TestRepositoryRetainsNewest4096ByMonotonicSequence|TestRepositoryRollsBackEvictionWhenInsertFails|TestInFlightAllowEvictionMakesTerminalAnnotationABenignMiss)$$' ./internal/storage ./internal/backup ./internal/grantrequests ./internal/authorization ./internal/catalog ./internal/invocation ./internal/composition

test-browser-s6-workflows:
	go test -race -count=1 -tags=e2e,browser -timeout=2m -run '^TestS6Browser(Protocol|FragmentStorage|AuthenticationEpoch|ReadGeneration|MutationState|ShellPrimitives|SecretSinks|Overview|Invocations|SystemStatus|ServerCatalogReads|ServerCreateUpdate|ServerOperations|ServerCredentials|AuthFlows|ServerDisconnectDelete|Principals|PrincipalCredentials|GrantReadsCreate|GrantCorrection|RequestReads|RequestAdjudication|AdminCredentials|Backups|CapabilityAudit)$$' ./test/e2e

test-browser-s6-visual:
	go test -race -count=1 -tags=e2e,browser -timeout=60s -run '^TestS6BrowserVisualChanged$$' ./test/e2e

test-browser-s6-a11y:
	go test -race -count=1 -tags=e2e,browser -timeout=45s -run '^TestS6BrowserAccessibilityChanged$$' ./test/e2e

test-browser-s6-cross:
	go test -race -count=1 -tags=e2e,browser -timeout=45s -run '^TestS6BrowserCross$$' ./test/e2e

test-cli-e2e-s6:
	go test -race -count=1 -tags=e2e -timeout=90s -run '^TestS6CLI(StatusInvocations|ServerCatalogReads|ServerCreateUpdate|ServerDelete|ServerOperations|ServerCredentials|AuthFlows|AdminCredentials|Backups|Principals|PrincipalCredentials|Grants|GrantRequests)$$' ./test/e2e

test-security-s6:
	go test -race -count=1 -tags=security -timeout=30s -run '^TestS6SecurityCanaries$$' ./test/security
	go test -race -count=1 -tags=e2e,browser -timeout=30s -run '^TestS6BrowserPrivacyCanary$$' ./test/e2e

qualify-external-s6:
	@go run ./test/acceptance/cmd --qualify-external

accept-s2-1:
	@go run ./test/acceptance/cmd --profile s2_1

accept-s3:
	@go run ./test/acceptance/cmd --profile s3

accept-s4:
	@go run ./test/acceptance/cmd --profile s4

accept-s5:
	@test -n "$(REPORT)" || (echo "REPORT is required" >&2; exit 2)
	@go run ./test/acceptance/cmd --profile s5 --output "$(REPORT)"

accept-s6:
	@test -n "$(REPORT)" || (echo "REPORT is required" >&2; exit 2)
	@go run ./test/acceptance/cmd --profile s6 --output "$(REPORT)"

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
