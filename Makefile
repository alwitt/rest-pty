LATEST_COMMIT_SHORT_SHA = $$(git rev-parse --short HEAD)
BASE_DIR = $(realpath .)
SHELL = bash

include .env
export

all: build

.PHONY: lint
lint: .prepare ## Lint the files
	@go mod tidy
	@revive ./...
	@golangci-lint run ./...

.PHONY: fix
fix: .prepare ## Lint and fix violations
	@go mod tidy
	@go fmt ./...
	@golangci-lint run --fix ./...

.PHONY: build
build: lint ## Build application
	@go build -o rest-pty .

.PHONY: demo
demo: lint ## Build demo application
	@docker build -t rest-pty-helper:latest -f poc/pty/Dockerfile ./poc/pty/
	@go build -o demo ./poc/pty/...

.PHONY: test
test: .prepare ## Run unit tests
	go test --count 1 -timeout 60s -short ./...

.PHONY: one-test
one-test: .prepare ## Run one unittest. Set `FILTER` as target test
	go test --count 1 -v -timeout 60s -run ^$(FILTER)$$ github.com/alwitt/rest-pty/...

.PHONY: test-package
test-package: .prepare ## Run all tests in a package. Set `PKG` as target package
	go test --count 1 -timeout 60s -short github.com/alwitt/rest-pty/$(PKG)/...

.PHONY: mock
mock: ## Define support mocks
	@mockery

.PHONY: doc
doc: .prepare ## Generate the OpenAPI spec
	@swag init -g main.go --parseDependency
	@rm docs/docs.go

.PHONY: ts-sdk
ts-sdk: .prepare ## Generate typescript client SDK
	@mkdir -vp tmp/sdk/ts-axios
	@docker run --rm \
	  --mount type=bind,source=$(BASE_DIR)/docs,target=/input,readonly \
	  --mount type=bind,source=$(BASE_DIR)/tmp,target=/output \
	  openapitools/openapi-generator-cli:latest-release \
	    generate -i /input/swagger.yaml -g typescript-axios -o /output/sdk/ts-axios

.PHONY: docker
docker: lint ## Build application docker image for local dev
	docker build \
		--load \
		-t "alwitt/rest-pty:latest" \
		-f docker/Dockerfile.rest-pty .

.PHONY: up
up: .prepare ## Start docker compose development stack
	docker compose -f docker/docker-compose.yml up -d

.PHONY: down
down: .prepare ## Stop docker compose development stack
	docker compose -f docker/docker-compose.yml down

.PHONY: gen-migrate
gen-migrate: ## Define new database migration
	atlas migrate diff \
	  --env gorm \
	  --format '{{ sql . "  " }}'

.PHONY: dev-migrate
dev-migrate: ## Test apply database migration to DEV Postgres
	atlas migrate apply \
	  --env gorm \
	  --url "postgres://postgres:postgres@localhost:4432/postgres?search_path=public&sslmode=disable"

.PHONY: api
api: build ## Run local dev API server
	./rest-pty -l info server -c poc/demo/server_cfg.yml --sql-pw postgres

.prepare: ## Prepare the project for local development
	@pip3 install pre-commit
	@pre-commit install
	@pre-commit install-hooks
	@touch .prepare

help: ## Display this help screen
	@grep -h -E '^[a-z0-9A-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-30s\033[0m %s\n", $$1, $$2}'
