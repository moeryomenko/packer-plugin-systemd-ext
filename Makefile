NAME := systemd-ext
BINARY := packer-plugin-$(NAME)
COVER_FILE ?= coverage.out
RACE_DETECTOR ?= -race
COUNT ?= 1
TEST ?= $(shell go list ./...)
IMPORT_PATH := $(shell go list -m -f {{.Path}} | head -1)
PLUGIN_FQN := $(IMPORT_PATH)

.PHONY: default
default: help

.PHONY: build
build: ## Build the plugin binary
	@go build -o $(BINARY) .

.PHONY: test
test: ## Run all tests with race detector and coverage
	@go tool gotestsum --format-hide-empty-pkg -f testname -- $(RACE_DETECTOR) -count $(COUNT) $(TEST) -timeout=3m -coverprofile=$(COVER_FILE)
	@go tool cover -func=$(COVER_FILE) | grep ^total

.PHONY: cover
cover: test ## Open coverage report in browser
	@go tool cover -html=$(COVER_FILE)

.PHONY: lint
lint: ## Run linter
	@go tool golangci-lint run -v --fix

.PHONY: fmt
fmt: ## Format Go source files
	@gofmt -s -w .
	@git diff --name-only -- '*.go' | xargs -r -I{} go tool golines --base-formatter=gofumpt --ignore-generated --tab-len=1 --max-len=120 -w {}
	@git diff --name-only -- '*.go' | xargs -r -I{} go tool goimports -local $(IMPORT_PATH) -w {}

.PHONY: vet
vet: ## Run go vet
	@go vet ./...

.PHONY: tidy
tidy: ## Tidy go module dependencies
	@go mod tidy -v

.PHONY: generate
generate: ## Run packer-sdc codegen + docs render (idempotent)
	@go generate ./...
	@rm -rf .docs
	@go run github.com/hashicorp/packer-plugin-sdk/cmd/packer-sdc renderdocs -src "docs" -partials docs-partials/ -dst ".docs/"
	@./.web-docs/scripts/compile-to-webdocs.sh "." ".docs" ".web-docs" "eryoma"
	@rm -rf .docs

.PHONY: generate-check
generate-check: ## Fail if a second generate changes tracked files
	@$(MAKE) generate
	@$(MAKE) generate
	@git diff --no-ext-diff --exit-code

.PHONY: dev
dev: ## Build with dev prerelease and install into packer
	@go build -ldflags="-X '$(PLUGIN_FQN)/version.VersionPrerelease=dev'" -o '$(BINARY)'
	packer plugins install --path $(BINARY) "$(shell echo '$(PLUGIN_FQN)' | sed 's/packer-plugin-//')"

.PHONY: e2e
e2e: ## Run the canonical local acceptance gate (bake then persist)
	@$(MAKE) e2e-bake
	@$(MAKE) e2e-persist

.PHONY: e2e-bake
e2e-bake: ## Run the bake-mode harness
	@test/e2e/bake/run.sh

.PHONY: e2e-persist
e2e-persist: ## Run the persist-mode harness
	@test/e2e/persist/run.sh

.PHONY: clean
clean: ## Remove build artifacts and coverage output
	@rm -f $(BINARY) $(COVER_FILE)

.PHONY: check
check: lint vet test ## Run lint, vet, and test (CI gate)

.PHONY: check-clean
check-clean: ## Fail if process-artifact identifiers appear in tracked files
	@scripts/check-no-traceability.sh

.PHONY: help
help: ## Print this help message
	@echo "Usage: make <target>"
	@echo ""
	@echo "Targets:"
	@grep -F -h '##' $(MAKEFILE_LIST) \
		| grep -F -v fgrep \
		| sort \
		| grep -E '^[a-zA-Z0-9_-]+:.*?## .*$$' \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2}'
