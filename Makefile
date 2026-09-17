BINARY  := mail-mcp
PKG     := ./cmd/mail-mcp
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
SWAG_VERSION := v1.16.6

.DEFAULT_GOAL := check

.PHONY: build
build: swagger ## Build the binary into bin/ and Swagger documents into doc/
	@mkdir -p bin
	go build -trimpath -ldflags "$(LDFLAGS)" -o bin/$(BINARY) $(PKG)

.PHONY: swagger
swagger: ## Generate doc/swagger.json and doc/swagger.yaml
	go run github.com/swaggo/swag/cmd/swag@$(SWAG_VERSION) init -g main.go -d cmd/mail-mcp,internal/httpx,internal/config --parseInternal --output doc --outputTypes json,yaml

.PHONY: run
run: ## Run against config.yml
	go run $(PKG) --config config.yml

.PHONY: test
test: ## Run the test suite
	go test ./...

.PHONY: cover
cover: ## Run tests and open a coverage report
	go test -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out

.PHONY: race
race: ## Run tests with the race detector
	go test -race ./...

.PHONY: fmt
fmt: ## Format all source
	gofmt -w .

.PHONY: vet
vet: ## Run go vet
	go vet ./...

.PHONY: tidy
tidy: ## Tidy go.mod
	go mod tidy

.PHONY: check
check: fmt vet test ## Format, vet, and test

.PHONY: docker
docker: ## Build the container image
	docker build --build-arg VERSION=$(VERSION) -t $(BINARY):$(VERSION) -t $(BINARY):latest .

.PHONY: token
token: ## Generate a backend management API key
	@openssl rand -hex 32

.PHONY: clean
clean: ## Remove build artifacts
	rm -rf bin dist coverage.out

.PHONY: help
help: ## List targets
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-10s\033[0m %s\n", $$1, $$2}'
