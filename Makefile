TOOLS := mcp-broker mcp-gateway sandbox-manager local-git-mcp local-gomod-proxy telegram-mcp http-broker
UNAME_S := $(shell uname -s)

.PHONY: install install-dev setup build test lint fmt tidy check audit $(TOOLS)

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

lint:
	@set -e; for dir in $(TOOLS); do $(MAKE) -C $$dir lint; done

fmt:
	@set -e; for dir in $(TOOLS); do $(MAKE) -C $$dir fmt; done

tidy:
	@set -e; for dir in $(TOOLS); do $(MAKE) -C $$dir tidy; done

check:
	npm run format:check
	$(MAKE) lint
	$(MAKE) test

audit:
	@set -e; for dir in $(TOOLS); do $(MAKE) -C $$dir audit; done
