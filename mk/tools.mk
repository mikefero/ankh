# --------------------------------------------------
# Tools tooling
# --------------------------------------------------

GOLANGCI_LINT_VERSION ?= v1.59.0

GOFILES := $(shell find $(APP_DIR) -name '*.go' ! -name '*_test.go')

# Ensure curl and gofumpt are available
ifeq (, $(shell which curl 2> /dev/null))
$(error "'curl' is not installed or available in PATH")
endif
ifeq (, $(shell which gofumpt 2> /dev/null))
$(error "'gofumpt' is not installed or available in PATH")
endif

.PHONY: install-tools
install-tools:
	@curl -sSfL \
		"https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh" \
		| sh -s -- -b "$(APP_DIR)/bin" "$(GOLANGCI_LINT_VERSION)"

.PHONY: lint
lint:
	@"$(APP_DIR)/bin/golangci-lint" run ./...

.PHONY: format
format:
	@gofumpt -w $(GOFILES)

