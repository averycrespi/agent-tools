STRESS_COUNT ?= 20
TEMPORARY_LISTEN ?= 127.0.0.1:8211
TEST_JSON ?= 0

ifneq ($(TEST_JSON),0)
ifneq ($(TEST_JSON),1)
$(error TEST_JSON must be 0 or 1)
endif
endif
SUITE_OUTPUT_FLAG := $(if $(filter 1,$(TEST_JSON)),--json)

.NOTPARALLEL: test test-browser test-frontend-development

.PHONY: help build install serve-temporary test test-unit test-integration test-harness test-material suite-inventory test-e2e test-security test-stress test-keyring-native test-serve-temporary test-browser test-browser-workflows test-browser-privacy test-browser-visual test-browser-accessibility test-browser-cross test-frontend-development test-frontend-development-node test-frontend-development-browser frontend-typecheck frontend-build frontend-verify-generated frontend-verify-supply-chain frontend-audit qualify-external-evidence accept adopt-acceptance-report lint fmt tidy verify audit

help:
	@printf '%s\n' \
		'test-unit | test-integration | test-harness | test-material | suite-inventory' \
		'test-e2e | test-security | test-stress | test-keyring-native | test-serve-temporary' \
		'test-browser-workflows | test-browser-privacy | test-browser-visual | test-browser-accessibility | test-browser-cross' \
		'test-browser (developer aggregate; one planner, isolated leaves)' \
		'TEST_JSON=1 emits JSON events for planned Go test commands' \
		'test-frontend-development-node | test-frontend-development-browser | test-frontend-development (developer aggregate)' \
		'frontend-typecheck | frontend-build | frontend-verify-generated | frontend-verify-supply-chain | frontend-audit' \
		'qualify-external-evidence' \
		'accept REPORT=<absolute-path>' \
		'adopt-acceptance-report REPORT=<absolute-path> ADOPTION=<absolute-path>' \
		'serve-temporary [TEMPORARY_LISTEN=127.0.0.1:8211]' \
		'build | install | lint | fmt | tidy | verify | audit'

build:
	go build -o mcp-gateway ./cmd/mcp-gateway

serve-temporary:
	./scripts/serve-temporary.sh --listen "$(TEMPORARY_LISTEN)"

install:
	GOBIN=$(shell go env GOPATH)/bin go install ./cmd/mcp-gateway

test: test-unit test-integration test-harness test-material test-serve-temporary

test-unit:
	go run ./test/acceptance/cmd run-suite test-unit $(SUITE_OUTPUT_FLAG)

test-harness:
	go run ./test/acceptance/cmd run-suite test-harness $(SUITE_OUTPUT_FLAG)

test-material:
	go run ./test/acceptance/cmd run-suite test-material $(SUITE_OUTPUT_FLAG)

suite-inventory:
	@go run ./test/acceptance/cmd suite-inventory

test-serve-temporary:
	./test/serve-temporary.sh

test-integration:
	go run ./test/acceptance/cmd run-suite test-integration $(SUITE_OUTPUT_FLAG)

test-e2e:
	go run ./test/acceptance/cmd run-suite test-e2e $(SUITE_OUTPUT_FLAG)

test-security:
	go run ./test/acceptance/cmd run-suite test-security $(SUITE_OUTPUT_FLAG)

test-stress:
	go run ./test/acceptance/cmd run-suite test-stress --count=$(STRESS_COUNT) $(SUITE_OUTPUT_FLAG)

test-keyring-native:
	./test/keyring-native.sh

test-browser:
	go run ./test/acceptance/cmd run-suite test-browser $(SUITE_OUTPUT_FLAG)

test-browser-workflows:
	go run ./test/acceptance/cmd run-suite test-browser-workflows $(SUITE_OUTPUT_FLAG)

test-browser-privacy:
	go run ./test/acceptance/cmd run-suite test-browser-privacy $(SUITE_OUTPUT_FLAG)

test-browser-visual:
	go run ./test/acceptance/cmd run-suite test-browser-visual $(SUITE_OUTPUT_FLAG)

test-browser-accessibility:
	go run ./test/acceptance/cmd run-suite test-browser-accessibility $(SUITE_OUTPUT_FLAG)

test-browser-cross:
	go run ./test/acceptance/cmd run-suite test-browser-cross $(SUITE_OUTPUT_FLAG)

test-frontend-development: test-frontend-development-node test-frontend-development-browser

test-frontend-development-node:
	npm --prefix .. run ui:test-dev

test-frontend-development-browser:
	go run ./test/acceptance/cmd run-suite test-frontend-development-browser $(SUITE_OUTPUT_FLAG)

frontend-typecheck:
	npm --prefix .. run ui:typecheck

frontend-build:
	npm --prefix .. run ui:build

frontend-verify-generated:
	npm --prefix .. run ui:verify-generated

frontend-verify-supply-chain:
	npm --prefix .. run ui:verify-supply-chain
	go run ./test/acceptance/cmd run-suite frontend-static-tests $(SUITE_OUTPUT_FLAG)

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
