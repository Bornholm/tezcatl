SHELL := /bin/bash

GIT_SHORT_VERSION ?= $(shell git describe --tags --abbrev=8 --always 2>/dev/null || echo dev)
GIT_LONG_VERSION ?= $(shell git describe --tags --abbrev=8 --dirty --always --long 2>/dev/null || echo dev)
LDFLAGS ?= -w -s \
	-X 'github.com/bornholm/tezcatl/internal/build.ShortVersion=$(GIT_SHORT_VERSION)' \
	-X 'github.com/bornholm/tezcatl/internal/build.LongVersion=$(GIT_LONG_VERSION)'

build:
	mkdir -p bin
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o bin/tezcatl ./cmd/tezcatl

test:
	go test -race -count=1 ./...

bench:
	go test -bench=. -benchmem -run '^$$' ./...

tidy:
	go mod tidy

.PHONY: build test bench tidy
