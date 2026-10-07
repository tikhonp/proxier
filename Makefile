APP_VERSION := $(shell git rev-parse --short HEAD 2>/dev/null || echo dev)
TEMPL_VERSION := v0.3.1020
GOVULNCHECK_VERSION := v1.8.0

.PHONY: dev build-dev fdev shell manage migrate-status test check templ editor check-editor lint vuln build build-image

# Development stack (air hot reload in Docker). Needs .env (see .env.example).
dev:
	docker compose up

build-dev:
	docker compose up --build

fdev:
	docker compose down

shell:
	docker exec -it proxier /bin/sh

# make manage ARGS="migrate status"
manage:
	docker exec -it proxier proxier manage $(ARGS)

migrate-status:
	docker exec -it proxier proxier manage migrate status

# Tests use temporary SQLite files: no services needed.
test:
	go test -race -count=1 ./...

templ:
	go run github.com/a-h/templ/cmd/templ@$(TEMPL_VERSION) generate

# The template editor's CodeMirror bundle (web/editor) is built in Docker, so
# the host needs no Node; the result is committed. Docker Desktop must be running.
editor:
	docker run --rm -v "$$PWD":/app -w /app/web/editor node:22-alpine sh ./build.sh

# Fails when the committed bundle is not what `make editor` builds.
check-editor: editor
	git diff --exit-code -- internal/platform/ui/static/js/editor.bundle.js

# Everything CI checks except lint and govulncheck, on the host.
check: templ check-editor
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "unformatted:"; echo "$$out"; exit 1; fi
	go vet ./...
	git diff --exit-code -- '*_templ.go'
	$(MAKE) test

lint:
	docker run --rm -v "$$PWD":/app -w /app golangci/golangci-lint:latest golangci-lint run

vuln:
	go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...

build:
	CGO_ENABLED=0 go build -ldflags "-X github.com/tikhonp/proxier/internal/platform/obs.AppVersion=$(APP_VERSION)" -o bin/proxier ./cmd/proxier

build-image:
	docker buildx build --build-arg APP_VERSION=$(APP_VERSION) -t ghcr.io/tikhonp/proxier:local --load .
