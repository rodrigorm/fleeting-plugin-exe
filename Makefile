.DEFAULT_GOAL := build

NAME := fleeting-plugin-exe
OUT := out/$(NAME)
CGO_ENABLED ?= 0
VERSION ?= 0.1.0-dev
REVISION := $(shell git rev-parse --short=8 HEAD 2>/dev/null || echo unknown)
REFERENCE := $(shell git branch --show-current 2>/dev/null || echo unknown)
BUILT_AT := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
PACKAGE := github.com/rodrigorm/fleeting-plugin-exe
LDFLAGS := -s -w \
	-X $(PACKAGE).VersionID=$(VERSION) \
	-X $(PACKAGE).Revision=$(REVISION) \
	-X $(PACKAGE).Reference=$(REFERENCE) \
	-X $(PACKAGE).BuiltAt=$(BUILT_AT)

.PHONY: build test fmt vet clean

build:
	@mkdir -p out
	CGO_ENABLED=$(CGO_ENABLED) go build -trimpath -ldflags "$(LDFLAGS)" -o $(OUT) ./cmd/$(NAME)

test:
	go test ./...

fmt:
	@test -z "$$(gofmt -l .)" || (gofmt -d . && exit 1)

vet:
	go vet ./...

clean:
	rm -rf out
