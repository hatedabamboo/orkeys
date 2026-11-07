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

build:
	go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BINARY) $(PKG)

install:
	go install $(GOFLAGS) -ldflags "$(LDFLAGS)" $(PKG)

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -w .

fmt-check:
	@out="$$(gofmt -l .)"; if [ -n "$$out" ]; then echo "not formatted:"; echo "$$out"; exit 1; fi

check: fmt-check vet test

lint:
	go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_VERSION) run

tidy:
	go mod tidy

release: clean
	@mkdir -p $(DIST)
	@for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; ext=; [ "$$os" = windows ] && ext=.exe; \
		out=$(DIST)/$(BINARY)-$(VERSION)-$$os-$$arch$$ext; \
		echo "build $$out"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $$out $(PKG) || exit 1; \
	done
	@cd $(DIST) && shasum -a 256 * > SHA256SUMS

snapshot:
	go run github.com/goreleaser/goreleaser/v2@$(GORELEASER_VERSION) release --snapshot --clean

clean:
	rm -rf $(BINARY) $(DIST)

bump-patch:
	@$(call bump,patch)

bump-minor:
	@$(call bump,minor)

bump-major:
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

help:
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk -F':.*?## ' '{printf "  %-10s %s\n", $$1, $$2}'
