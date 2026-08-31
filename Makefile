TOOLS := mcp-broker sandbox-manager local-git-mcp local-gomod-proxy telegram-mcp http-broker
INTEGRATION_TOOLS := mcp-broker local-git-mcp local-gomod-proxy
E2E_TOOLS := mcp-broker local-gomod-proxy http-broker
UNAME_S := $(shell uname -s)

.PHONY: install install-dev setup build test test-integration test-e2e lint fmt fmt-check tidy check vulncheck audit $(TOOLS)

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

check: fmt-check lint test

vulncheck:
	@set -e; for dir in $(TOOLS); do go -C $$dir tool govulncheck ./...; done

audit:
	@set -e; for dir in $(TOOLS); do $(MAKE) -C $$dir audit; done
