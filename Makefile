SHELL := /bin/bash
.SHELLFLAGS := -euo pipefail -c

# Every module go.work names, as the package pattern covering it. A module
# added to go.work is covered with no change here.
PATTERNS = $(addsuffix /...,$(shell go list -m))

# Names the packages the pattern selected and fails when it selected none.
# Modules that hold no package yet match nothing, which go reports as a warning.
define count_packages
n=$$(go list $(PATTERNS) | wc -l); \
echo "$(1): $$n packages selected by <module>/... for each of the $(words $(PATTERNS)) modules go.work names"; \
test "$$n" -gt 0
endef

# The Go files git tracks or would track, NUL separated.
GO_FILES = git ls-files --cached --others --exclude-standard -z -- '*.go'

.PHONY: lint format format-apply test build ci

lint:
	@$(call count_packages,lint)
	go vet $(PATTERNS)

format:
	@n=$$($(GO_FILES) | tr -cd '\0' | wc -c); \
	echo "format: $$n Go files from git ls-files --cached --others --exclude-standard"; \
	test "$$n" -gt 0; \
	out=$$($(GO_FILES) | xargs -0 gofmt -l); \
	if [ -n "$$out" ]; then echo "needs gofmt:"; echo "$$out"; exit 1; fi

format-apply:
	$(GO_FILES) | xargs -0 gofmt -w

test:
	@$(call count_packages,test)
	go test $(PATTERNS)

build:
	@$(call count_packages,build)
	go build $(PATTERNS)

ci: lint format test build
