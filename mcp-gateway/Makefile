STRESS_COUNT ?= 20

.NOTPARALLEL: test test-browser test-frontend-development

.PHONY: help build install test test-unit test-integration test-e2e test-security test-stress test-keyring-native test-browser test-browser-workflows test-browser-privacy test-browser-visual test-browser-accessibility test-browser-cross test-frontend-development test-frontend-development-node test-frontend-development-browser frontend-typecheck frontend-build frontend-verify-generated frontend-verify-supply-chain frontend-audit qualify-external-evidence accept adopt-acceptance-report lint fmt tidy verify audit

help:
	@printf '%s\n' \
		'test-unit | test-integration | test-e2e | test-security | test-stress | test-keyring-native' \
		'test-browser-workflows | test-browser-privacy | test-browser-visual | test-browser-accessibility | test-browser-cross' \
		'test-browser (developer aggregate)' \
		'test-frontend-development-node | test-frontend-development-browser | test-frontend-development (developer aggregate)' \
		'frontend-typecheck | frontend-build | frontend-verify-generated | frontend-verify-supply-chain | frontend-audit' \
		'qualify-external-evidence' \
		'accept REPORT=<absolute-path>' \
		'adopt-acceptance-report REPORT=<absolute-path> ADOPTION=<absolute-path>' \
		'build | install | lint | fmt | tidy | verify | audit'

build:
	go build -o mcp-gateway ./cmd/mcp-gateway

install:
	GOBIN=$(shell go env GOPATH)/bin go install ./cmd/mcp-gateway

test: test-unit test-integration

test-unit:
	go test -race -count=1 -timeout=90s ./...

test-integration:
	go test -race -count=1 -tags=integration -timeout=60s -run '^(Test.*Integration|TestRequestSchemaMigrationUsesRealSQLite|TestRestoreAcceptedSchemaLineages|TestConfiguredConnectionsEnforcePragmasAndFiniteBusyDeadline|TestBusyBeyondDeadlineLatchesMutationAcrossRestart|TestRepositoryRetainsNewest4096ByMonotonicSequence|TestRepositoryRollsBackEvictionWhenInsertFails|TestInFlightAllowEvictionMakesTerminalAnnotationABenignMiss|TestSchemaSevenFixtureMigratesWithRealSQLite|TestPopulatedSchemaEightMigratesToNineWithRealSQLite|TestServerSchemaAndBackupContainNoSecretOrTransientRepresentation|TestServerSnapshotWatermarkExcludesLaterInsert|TestServicePublishesOneCompleteStaticGenerationAndSafeOperation)$$' ./internal/storage ./internal/backup ./internal/servers ./internal/servercredentials ./internal/grantrequests ./internal/authorization ./internal/catalog ./internal/invocation ./internal/composition

test-e2e:
	go test -race -count=1 -tags=e2e -timeout=2m ./test/e2e/...

test-security:
	go test -race -count=1 -tags=security -timeout=30s -run '^(TestReleaseReportSecretSinkBoundaries|TestDurableSecretSinkBoundaries|TestSecurityEvidenceOwnerManifest|TestStaticSecretSinkClosure)$$' ./test/security/... ./test/acceptance

test-stress:
	go test -race -tags=stress -count=$(STRESS_COUNT) -timeout=2m -run '^(TestConcurrentSemanticDeduplicationStress|TestApprovalCancellationPolicyLinearizationStress)$$' ./internal/grantrequests
	go test -race -tags=stress -count=$(STRESS_COUNT) -timeout=90s -run '^(TestSyntheticSnapshotPaginationStress|TestLostLocalMutationResponseDeduplicatesExplicitRetryStress)$$' ./internal/selfservice
	go test -race -tags=stress -count=$(STRESS_COUNT) -timeout=90s -run '^TestLocalInvocationDrainCleanupStress$$' ./internal/composition

test-keyring-native:
	./test/keyring-native.sh

test-browser: test-browser-workflows test-browser-privacy test-browser-visual test-browser-accessibility test-browser-cross

test-browser-workflows:
	go test -race -count=1 -tags=e2e,browser -timeout=3m -run '^TestBrowser(Protocol|FragmentStorage|AuthenticationEpoch|ReadGeneration|MutationState|ShellPrimitives|SecretSinks|Overview|Invocations|SystemStatus|ServerCatalogReads|ServerCreateUpdate|ServerOperations|ServerCredentials|AuthFlows|ServerDisconnectDelete|Principals|PrincipalCredentials|GrantReadsCreate|GrantCorrection|RequestReads|RequestAdjudication|AdminCredentials|Backups|CapabilityAudit|SessionLifecycleCanary|PriorSessionResponseIsolationCanary|OverviewInvocationSystemCanary|ServerManagementCanary|AccessManagementReadCanary|SystemAdministrationCanary|VisualAccessibilityPrivacyCanary|Coordinator)$$' ./test/e2e

test-browser-privacy:
	go test -race -count=1 -tags=e2e,browser -timeout=30s -run '^TestBrowserSecretStoragePrivacy$$' ./test/e2e

test-browser-visual:
	go test -race -count=1 -tags=e2e,browser -timeout=60s -run '^TestBrowserVisualResponsiveMatrix$$' ./test/e2e

test-browser-accessibility:
	go test -race -count=1 -tags=e2e,browser -timeout=45s -run '^TestBrowserAccessibility$$' ./test/e2e

test-browser-cross:
	go test -race -count=1 -tags=e2e,browser -timeout=45s -run '^TestBrowserCrossCompatibility$$' ./test/e2e

test-frontend-development: test-frontend-development-node test-frontend-development-browser

test-frontend-development-node:
	npm --prefix .. run ui:test-dev

test-frontend-development-browser:
	go test -race -count=1 -tags=e2e,browser -timeout=2m -run '^TestFrontendDevelopment(LiveReload|ControlPlane)$$' ./test/e2e

frontend-typecheck:
	npm --prefix .. run ui:typecheck

frontend-build:
	npm --prefix .. run ui:build

frontend-verify-generated:
	npm --prefix .. run ui:verify-generated

frontend-verify-supply-chain:
	npm --prefix .. run ui:verify-supply-chain
	go test -race -count=1 -tags=frontend -timeout=30s -run '^TestStaticSupplyChain$$' ./internal/api

frontend-audit:
	npm --prefix .. run ui:audit

qualify-external-evidence:
	@go run ./test/acceptance/cmd qualify-external-evidence

accept:
	@case "$(REPORT)" in /*) ;; *) echo 'REPORT must be an absolute path' >&2; exit 2;; esac
	@go run ./test/acceptance/cmd accept --report "$(REPORT)"

adopt-acceptance-report:
	@case "$(REPORT)" in /*) ;; *) echo 'REPORT must be an absolute path' >&2; exit 2;; esac
	@case "$(ADOPTION)" in /*) ;; *) echo 'ADOPTION must be an absolute path' >&2; exit 2;; esac
	@go run ./test/acceptance/cmd adopt-acceptance-report --report "$(REPORT)" --output "$(ADOPTION)"

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
	go tool govulncheck ./...
