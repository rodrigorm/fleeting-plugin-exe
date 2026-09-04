.DEFAULT_GOAL := build

NAME := fleeting-plugin-exe
OUT := out/$(NAME)
CGO_ENABLED ?= 0

.PHONY: build test fmt vet clean

build:
	@mkdir -p out
	CGO_ENABLED=$(CGO_ENABLED) go build -trimpath -o $(OUT) ./cmd/$(NAME)

test:
	go test ./...

fmt:
	@test -z "$$(gofmt -l .)" || (gofmt -d . && exit 1)

vet:
	go vet ./...

clean:
	rm -rf out
