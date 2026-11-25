# AdminDescargas: tareas de desarrollo.
# Requiere Go 1.27, Node >= 18 (Vite 6) y, para `lint`, golangci-lint v2.

BINARY   := adesc
PKG      := admindescargas
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT   ?= $(shell git rev-parse --short HEAD 2>/dev/null)
DATE     ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS  := -s -w \
	-X $(PKG)/internal/buildinfo.Version=$(VERSION) \
	-X $(PKG)/internal/buildinfo.Commit=$(COMMIT) \
	-X $(PKG)/internal/buildinfo.Date=$(DATE)
GOFLAGS  := -trimpath

PLATFORMS := linux/amd64 linux/arm64 windows/amd64 windows/arm64 darwin/amd64 darwin/arm64

.PHONY: all web web-placeholder build desktop test test-race live lint lint-desktop vet fmt tidy deps-update cross clean

all: web build

## web: instala dependencias y compila la Web UI en web/dist
web:
	cd web && npm ci && npm run build

## web-placeholder: sustituye web/dist por la página provisional (sin git)
web-placeholder:
	rm -rf web/dist
	mkdir -p web/dist
	cp tools/web-placeholder.html web/dist/index.html

## build: daemon + CLI sin cgo
build:
	CGO_ENABLED=0 go build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o bin/$(BINARY) ./cmd/adesc

## desktop: app de escritorio (Wails v3) con GTK 3 y WebKitGTK 4.1
desktop:
	mkdir -p cmd/adesc-desktop/build/bin
	go -C cmd/adesc-desktop build -tags desktop,production,gtk3 -o build/bin/adesc-desktop .

test:
	go test ./...

test-race:
	go test -race ./...

## live: tests contra los hosters reales (necesitan red y variables de entorno)
live:
	go test -tags live ./internal/hosters/...

vet:
	go vet ./...

lint:
	golangci-lint run ./...

lint-desktop:
	cd cmd/adesc-desktop && golangci-lint run --build-tags desktop,production,gtk3 ./...

fmt:
	gofmt -w cmd internal web/embed.go

## tidy: go mod tidy y comprobación de que internal/deps sigue compilando
tidy:
	go mod tidy
	CGO_ENABLED=0 go build -tags tools ./internal/deps/

## deps-update: actualiza las dependencias de los dos módulos y vuelve a fijar
## gvisor (el que pide wireguard-go) y go-retry (el que pide goose); ver CONTRIBUTING.md
GVISOR_PIN  := v0.0.0-20250503011706-39ed1f5ac29c
GORETRY_PIN := v0.5.0
deps-update:
	go get -u ./...
	go get gvisor.dev/gvisor@$(GVISOR_PIN) github.com/sethvargo/go-retry@$(GORETRY_PIN)
	go mod tidy
	cd cmd/adesc-desktop && go get -u ./... && go get gvisor.dev/gvisor@$(GVISOR_PIN) github.com/sethvargo/go-retry@$(GORETRY_PIN) && go mod tidy
	CGO_ENABLED=0 go build -tags tools ./internal/deps/

## cross: binarios del daemon para todas las plataformas en bin/
cross:
	@set -e; for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; ext=; [ $$os = windows ] && ext=.exe; \
		echo "-> $$os/$$arch"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build $(GOFLAGS) -ldflags '$(LDFLAGS)' \
			-o bin/$(BINARY)-$$os-$$arch$$ext ./cmd/adesc; \
	done

clean:
	go clean
	$(RM) -r bin dist
