TOOLS := mcp-broker mcp-gateway sandbox-manager local-git-mcp local-gomod-proxy telegram-mcp http-broker
OTHER_TOOLS := mcp-broker sandbox-manager local-git-mcp local-gomod-proxy telegram-mcp http-broker
INTEGRATION_TOOLS := mcp-broker mcp-gateway local-git-mcp local-gomod-proxy
E2E_TOOLS := mcp-broker mcp-gateway local-gomod-proxy http-broker
UNAME_S := $(shell uname -s)

.PHONY: help install install-dev setup build test test-ci test-integration test-e2e lint fmt fmt-check tidy check vulncheck check-other-tools test-browser test-frontend-development frontend-typecheck frontend-build frontend-verify-generated frontend-verify-supply-chain frontend-audit qualify-external-evidence accept adopt-acceptance-report audit $(TOOLS)

help:
	@$(MAKE) -s -C mcp-gateway help

install:
	@set -e; for dir in $(TOOLS); do $(MAKE) -C $$dir install; done

install-dev:
	npm install

setup:
ifeq ($(UNAME_S),Darwin)
	brew bundle
else
	@echo "Skipping brew bundle on $(UNAME_S); install system dependencies manually."
endif
	$(MAKE) install-dev
	$(MAKE) install

build:
	@set -e; for dir in $(TOOLS); do $(MAKE) -C $$dir build; done

test:
	@set -e; for dir in $(TOOLS); do $(MAKE) -C $$dir test; done

test-ci:
	python3 -B -m unittest discover -s .github/scripts -p '*_test.py'

test-integration:
	@set -e; for dir in $(INTEGRATION_TOOLS); do $(MAKE) -C $$dir test-integration; done

test-e2e:
	@set -e; for dir in $(E2E_TOOLS); do $(MAKE) -C $$dir test-e2e; done

lint:
	@set -e; for dir in $(TOOLS); do $(MAKE) -C $$dir lint; done

fmt:
	@set -e; for dir in $(TOOLS); do $(MAKE) -C $$dir fmt; done

fmt-check:
	npm run format:check
	@set -e; files="$$(for dir in $(TOOLS); do go -C $$dir tool goimports -l .; done)"; \
		test -z "$$files" || { printf '%s\n' "Go files require formatting:" "$$files"; exit 1; }

tidy:
	@set -e; for dir in $(TOOLS); do $(MAKE) -C $$dir tidy; done

check: test-ci fmt-check lint test

vulncheck:
	@set -e; for dir in $(TOOLS); do go -C $$dir tool govulncheck ./...; done

check-other-tools:
	@set -e; for dir in $(OTHER_TOOLS); do $(MAKE) -C $$dir lint; $(MAKE) -C $$dir test; done

test-browser:
	$(MAKE) -C mcp-gateway test-browser

test-frontend-development:
	$(MAKE) -C mcp-gateway test-frontend-development

frontend-typecheck:
	$(MAKE) -C mcp-gateway frontend-typecheck

frontend-build:
	$(MAKE) -C mcp-gateway frontend-build

frontend-verify-generated:
	$(MAKE) -C mcp-gateway frontend-verify-generated

frontend-verify-supply-chain:
	$(MAKE) -C mcp-gateway frontend-verify-supply-chain

frontend-audit:
	$(MAKE) -C mcp-gateway frontend-audit

qualify-external-evidence:
	$(MAKE) -C mcp-gateway qualify-external-evidence

accept:
	$(MAKE) -C mcp-gateway accept REPORT="$(REPORT)"

adopt-acceptance-report:
	$(MAKE) -C mcp-gateway adopt-acceptance-report REPORT="$(REPORT)" ADOPTION="$(ADOPTION)"

audit:
	@set -e; for dir in $(TOOLS); do $(MAKE) -C $$dir audit; done
