TOOLS := mcp-broker agent-gateway local-git-mcp http-broker typesafe-mcp
OTHER_TOOLS := $(filter-out agent-gateway,$(TOOLS))
INTEGRATION_TOOLS := mcp-broker agent-gateway local-git-mcp typesafe-mcp
E2E_TOOLS := mcp-broker agent-gateway http-broker
LOCAL_TEST_JOBS ?= 2

ifneq ($(LOCAL_TEST_JOBS),1)
ifneq ($(LOCAL_TEST_JOBS),2)
$(error LOCAL_TEST_JOBS must be 1 or 2)
endif
endif

# .NOTPARALLEL is global on older Make, so omit it in the internal worker invocation.
ifeq ($(filter __test-%,$(MAKECMDGOALS)),)
.NOTPARALLEL:
endif

.PHONY: help install install-dev setup build test test-ci test-integration test-e2e lint fmt fmt-check tidy check vulncheck check-other-tools test-browser test-frontend-development frontend-typecheck frontend-build frontend-verify-generated frontend-verify-supply-chain frontend-audit qualify-external-evidence accept adopt-acceptance-report audit $(TOOLS)

help:
	@printf '%s\n' 'LOCAL_TEST_JOBS=1|2 bounds non-Gateway ordinary tests; Gateway and linters stay isolated'
	@$(MAKE) -s -C agent-gateway help

install:
	@set -e; for dir in $(TOOLS); do $(MAKE) -C $$dir install; done

install-dev:
	npm install

setup:
	$(MAKE) install-dev
	$(MAKE) install

build:
	@set -e; for dir in $(TOOLS); do $(MAKE) -C $$dir build; done

# Gateway's harness releases and rebinds ports, so it must not overlap other listeners.
test:
	$(MAKE) -C agent-gateway test
	$(MAKE) -S -j$(LOCAL_TEST_JOBS) $(addprefix __test-,$(OTHER_TOOLS))

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
	@set -e; for dir in $(OTHER_TOOLS); do $(MAKE) -C $$dir lint; done
	$(MAKE) -S -j$(LOCAL_TEST_JOBS) $(addprefix __test-,$(OTHER_TOOLS))

test-browser:
	$(MAKE) -C agent-gateway test-browser

test-frontend-development:
	$(MAKE) -C agent-gateway test-frontend-development

frontend-typecheck:
	$(MAKE) -C agent-gateway frontend-typecheck

frontend-build:
	$(MAKE) -C agent-gateway frontend-build

frontend-verify-generated:
	$(MAKE) -C agent-gateway frontend-verify-generated

frontend-verify-supply-chain:
	$(MAKE) -C agent-gateway frontend-verify-supply-chain

frontend-audit:
	$(MAKE) -C agent-gateway frontend-audit

qualify-external-evidence:
	$(MAKE) -C agent-gateway qualify-external-evidence

accept:
	$(MAKE) -C agent-gateway accept REPORT="$(REPORT)"

adopt-acceptance-report:
	$(MAKE) -C agent-gateway adopt-acceptance-report REPORT="$(REPORT)" ADOPTION="$(ADOPTION)"

audit:
	@set -e; for dir in $(TOOLS); do $(MAKE) -C $$dir audit; done

.PHONY: $(addprefix __test-,$(OTHER_TOOLS))
$(addprefix __test-,$(OTHER_TOOLS)): __test-%:
	$(MAKE) -C $* test
