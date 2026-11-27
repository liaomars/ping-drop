SHELL := /bin/zsh
GO_DOCKER_IMAGE ?= golang:1.23
GO_MOD_VOL ?= pingdrop-gomod
GO_CACHE_VOL ?= pingdrop-gocache
DOCKER_BASE = docker run --rm \
	-v "$(CURDIR):/app" \
	-v "$(GO_MOD_VOL):/go/pkg/mod" \
	-v "$(GO_CACHE_VOL):/root/.cache/go-build" \
	-w /app
DOCKER_RUN = $(DOCKER_BASE) $(GO_DOCKER_IMAGE)

.PHONY: test test-go test-web build build-web build-linux vet tidy dev

tidy:
	$(DOCKER_RUN) go mod tidy

vet:
	$(DOCKER_RUN) go vet ./...

test-go:
	$(DOCKER_RUN) go test -race -v ./...

test-web:
	pnpm --dir web exec vue-tsc --noEmit
	pnpm --dir web test

test: test-web vet test-go

build-web:
	pnpm --dir web install --frozen-lockfile || pnpm --dir web install
	pnpm --dir web build
	git checkout -- web/dist/.gitkeep 2>/dev/null || touch web/dist/.gitkeep

build: build-web
	$(DOCKER_RUN) sh -c 'CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o bin/ping-drop ./cmd/ping-drop'

build-linux: build-web
	$(DOCKER_RUN) sh -c 'CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o bin/ping-drop-linux-amd64 ./cmd/ping-drop'

dev:
	$(DOCKER_BASE) -p 18080:8080 $(GO_DOCKER_IMAGE) go run ./cmd/ping-drop --addr :8080
