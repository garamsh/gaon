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

.PHONY: lint format format-apply test build ci e2e e2e-extensions

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

# go test prints only `ok <pkg>` for a package that passes, so the target reads
# the event stream (-json) and reports the tests skipped per package and in
# total through tooling/test-report.sh, which needs jq. The exit status is go
# test's, kept by pipefail.
test:
	@$(call count_packages,test)
	go test -json $(PATTERNS) | tooling/test-report.sh

build:
	@$(call count_packages,build)
	go build $(PATTERNS)

ci: lint format test build e2e-extensions

# The end-to-end test of M1 on QEMU Nodes. It is not part of ci: it needs /dev/kvm
# with nested virtualization (docs/architecture/e2e.md). E2E_DISABLE names services
# naru-cluster leaves out, for the run that shows the test fails without them.
E2E_DIR ?= $(CURDIR)/build/e2e
E2E_DISABLE ?=
NARU_COMMIT = 5b42b429c6b41447f2ea267fe606c2b5d44ea63e
NARU_RELEASE = v0.3.0-rc.1
GAON_SERVICES = gaon-apiserver:./apiserver/cmd/gaon-apiserver gaon-scheduler:./scheduler/cmd/gaon-scheduler gaon-controllers:./controllers/cmd/gaon-controllers gaon-node-agent:./node-agent/cmd/gaon-node-agent

e2e:
	rm -rf "$(E2E_DIR)/cluster" "$(E2E_DIR)/services"
	mkdir -p "$(E2E_DIR)/tools" "$(E2E_DIR)/release" "$(E2E_DIR)/services"
	for s in $(GAON_SERVICES); do \
		CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o "$(E2E_DIR)/services/$${s%%:*}" "$${s#*:}"; \
	done
	go build -o "$(E2E_DIR)/gaon" ./gaon-cli/cmd/gaon
	GOBIN="$(E2E_DIR)/tools" go install github.com/garamsh/naru/tooling/qemu/cmd/naru-cluster@$(NARU_COMMIT)
	gh release download $(NARU_RELEASE) -R garamsh/naru -D "$(E2E_DIR)/release" --clobber
	cd "$(E2E_DIR)/release" && sha256sum --check --quiet SHA256SUMS
	echo "e2e: gaon binaries built from $$(git rev-parse HEAD) with naru $(NARU_RELEASE) at $(NARU_COMMIT); disabled services: '$(E2E_DISABLE)'"
	cd tooling/e2e && go vet -tags e2e ./...
	status=0; cd tooling/e2e && E2E_NARU_CLUSTER="$(E2E_DIR)/tools/naru-cluster" E2E_RELEASE="$(E2E_DIR)/release" \
		E2E_SERVICES="$(E2E_DIR)/services" E2E_GAON="$(E2E_DIR)/gaon" E2E_DIR="$(E2E_DIR)" E2E_DISABLE="$(E2E_DISABLE)" \
		go test -tags e2e -count=1 -v -timeout 30m -run '^TestFailover_' ./tests >"$(E2E_DIR)/test.log" 2>&1 || status=$$?; \
		tail -n 40 "$(E2E_DIR)/test.log"; \
		n=$$(grep -c '^--- PASS' "$(E2E_DIR)/test.log" || true); \
		echo "e2e: $$n test(s) passed, selected by -run '^TestFailover_' with -tags e2e; exit $$status"; \
		test $$status -eq 0 -a "$$n" -gt 0

# The extension points under RBAC on this host, with no KVM: the built services
# run against embedded etcd and a test-served mvmd (docs/architecture/e2e.md).
# The build tag keeps the test out of lint and test, so vet compiles it here.
EXT_DIR ?= $(CURDIR)/build/e2e-extensions
# The extension test also runs the workloads unit and applies its role with the CLI.
EXT_SERVICES = $(GAON_SERVICES) gaon-workloads:./workloads/cmd/gaon-workloads gaon:./gaon-cli/cmd/gaon

e2e-extensions:
	rm -rf "$(EXT_DIR)"
	mkdir -p "$(EXT_DIR)/services"
	for s in $(EXT_SERVICES); do \
		go build -o "$(EXT_DIR)/services/$${s%%:*}" "$${s#*:}"; \
	done
	cd tooling/e2e && go vet -tags extensions ./extensions
	status=0; cd tooling/e2e && E2E_EXT_SERVICES="$(EXT_DIR)/services" \
		go test -tags extensions -count=1 -v -timeout 10m ./extensions >"$(EXT_DIR)/test.log" 2>&1 || status=$$?; \
		tail -n 40 "$(EXT_DIR)/test.log"; \
		n=$$(grep -c '^--- PASS' "$(EXT_DIR)/test.log" || true); \
		echo "e2e-extensions: $$n test(s) passed, selected by -tags extensions ./extensions; exit $$status"; \
		test $$status -eq 0 -a "$$n" -gt 0
