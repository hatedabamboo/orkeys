BINARY   := orkeys
PKG      := .
DIST     := dist
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS  := -s -w -X main.version=$(VERSION)
GOFLAGS  := -trimpath
GOLANGCI_VERSION   ?= v2.4.0
GORELEASER_VERSION ?= v2.12.0
PLATFORMS := darwin/amd64 darwin/arm64 linux/amd64 linux/arm64 windows/amd64

.DEFAULT_GOAL := build
.PHONY: build install test vet fmt fmt-check check lint tidy release snapshot clean help bump-patch bump-minor bump-major

build: ## Build the binary for this machine
	go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BINARY) $(PKG)

install: ## Install into $(go env GOPATH)/bin
	go install $(GOFLAGS) -ldflags "$(LDFLAGS)" $(PKG)

test: ## Run unit tests
	go test ./...

vet: ## Run go vet
	go vet ./...

fmt: ## Format the code
	gofmt -w .

fmt-check: ## Fail if code is not formatted
	@out="$$(gofmt -l .)"; if [ -n "$$out" ]; then echo "not formatted:"; echo "$$out"; exit 1; fi

check: fmt-check vet test ## Run format check, vet and tests

lint: ## Run golangci-lint via go run
	go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_VERSION) run

tidy: ## Tidy go.mod and go.sum
	go mod tidy

release: clean ## Cross-compile into dist/ for all PLATFORMS
	@mkdir -p $(DIST)
	@for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; ext=; [ "$$os" = windows ] && ext=.exe; \
		out=$(DIST)/$(BINARY)-$(VERSION)-$$os-$$arch$$ext; \
		echo "build $$out"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $$out $(PKG) || exit 1; \
	done
	@cd $(DIST) && shasum -a 256 * > SHA256SUMS

snapshot: ## Local GoReleaser build into dist/, no publish
	go run github.com/goreleaser/goreleaser/v2@$(GORELEASER_VERSION) release --snapshot --clean

clean: ## Remove build output
	rm -rf $(BINARY) $(DIST)

bump-patch: ## Bump patch version in VERSION (v1.2.3 -> v1.2.4)
	@$(call bump,patch)

bump-minor: ## Bump minor version in VERSION (v1.2.3 -> v1.3.0)
	@$(call bump,minor)

bump-major: ## Bump major version in VERSION (v1.2.3 -> v2.0.0)
	@$(call bump,major)

define bump
	cur="$$(tr -d '[:space:]' < VERSION)"; \
	echo "$$cur" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+$$' || { echo "VERSION is not vX.Y.Z: $$cur"; exit 1; }; \
	v="$${cur#v}"; maj="$${v%%.*}"; rest="$${v#*.}"; min="$${rest%%.*}"; pat="$${rest#*.}"; \
	case "$(1)" in \
		major) maj=$$((maj+1)); min=0; pat=0 ;; \
		minor) min=$$((min+1)); pat=0 ;; \
		patch) pat=$$((pat+1)) ;; \
	esac; \
	new="v$$maj.$$min.$$pat"; \
	echo "$$new" > VERSION; \
	echo "$$cur -> $$new"
endef

help: ## Show targets
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk -F':.*?## ' '{printf "  %-10s %s\n", $$1, $$2}'
