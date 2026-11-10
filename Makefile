# jonus — local AI music generation (CLI + web UI)
#
# Two mirrors are baked in because the upstream endpoints are unreachable from
# some networks: Go modules go through goproxy.cn, model weights through
# hf-mirror.com. Override them on the command line if your network differs.

GOPROXY_URL := https://goproxy.cn,direct
GOSUMDB_URL := sum.golang.google.cn
HF_MIRROR   := https://hf-mirror.com

BIN       := bin/jonus
WEB_DIR   := web
EMBED_DIR := internal/webui/dist
PY_DIR    := python

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
GOFLAGS := -trimpath
LDFLAGS := -s -w -X github.com/shaolianbo/jonus/internal/cli.Version=$(VERSION)

.DEFAULT_GOAL := help

.PHONY: help goenv setup venv models web build build-go run dev test vet fmt lint doctor clean

help: ## Show available targets
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-10s\033[0m %s\n", $$1, $$2}'

goenv: ## Point the Go toolchain at a reachable module proxy
	go env -w GOPROXY=$(GOPROXY_URL) GOSUMDB=$(GOSUMDB_URL)

setup: goenv ## Install all dependencies (Go modules, npm packages, Python env)
	go mod download
	@if [ -f $(WEB_DIR)/package.json ]; then cd $(WEB_DIR) && npm ci; fi
	@if [ -f $(PY_DIR)/pyproject.toml ]; then $(MAKE) venv; fi

venv: ## Create the Python sidecar environment (pinned to 3.12)
	uv sync --project $(PY_DIR) --python 3.12

models: ## Download the ACE-Step weights through the mirror
	HF_ENDPOINT=$(HF_MIRROR) uv run --project $(PY_DIR) python -m acestep.download

web: ## Build the Svelte frontend into the Go embed directory
	cd $(WEB_DIR) && npm run build

build: ## Build the jonus binary (rebuilds the frontend first when present)
	@if [ -f $(WEB_DIR)/package.json ]; then $(MAKE) web; fi
	$(MAKE) build-go

build-go: ## Build the Go binary only, reusing whatever is already embedded
	go build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o $(BIN) ./cmd/jonus

run: build ## Build and serve
	./$(BIN) serve --open

dev: ## Run the API with Vite hot reload (web UI on :5173, API on :7777)
	@if [ ! -f $(WEB_DIR)/package.json ]; then echo "no frontend yet; run 'make build-go && ./$(BIN) serve'"; exit 1; fi
	cd $(WEB_DIR) && npm run dev & \
	go run ./cmd/jonus serve --dev --port 7777

test: ## Run the test suite
	go test ./...

vet: ## Run go vet
	go vet ./...

fmt: ## Format the Go source
	gofmt -s -w .

lint: ## Run golangci-lint if it is installed
	@command -v golangci-lint >/dev/null 2>&1 || { echo "golangci-lint not installed"; exit 1; }
	golangci-lint run

doctor: build-go ## Report what this machine is ready for
	./$(BIN) doctor

clean: ## Remove build output and the local database
	rm -rf bin
	git clean -fdX -- $(EMBED_DIR) 2>/dev/null || true
