TOOLS := worktree-manager mcp-broker sandbox-manager local-git-mcp local-gh-mcp broker-cli

.PHONY: install build test lint fmt tidy audit $(TOOLS)

install:
	@for dir in $(TOOLS); do $(MAKE) -C $$dir install; done

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

audit:
	@for dir in $(TOOLS); do $(MAKE) -C $$dir audit; done
