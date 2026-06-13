TOOLS := worktree-manager mcp-broker sandbox-manager local-git-mcp local-gomod-proxy pi-dispatcher pi-orchestrator
UNAME_S := $(shell uname -s)

.PHONY: install install-dev setup build test lint fmt tidy check audit $(TOOLS)

install:
	@for dir in $(TOOLS); do $(MAKE) -C $$dir install; done

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
	@for dir in $(TOOLS); do $(MAKE) -C $$dir build; done

test:
	@for dir in $(TOOLS); do $(MAKE) -C $$dir test; done

lint:
	@for dir in $(TOOLS); do $(MAKE) -C $$dir lint; done

fmt:
	@for dir in $(TOOLS); do $(MAKE) -C $$dir fmt; done

tidy:
	@for dir in $(TOOLS); do $(MAKE) -C $$dir tidy; done

check:
	npm run format:check
	$(MAKE) lint
	$(MAKE) test

audit:
	@for dir in $(TOOLS); do $(MAKE) -C $$dir audit; done
