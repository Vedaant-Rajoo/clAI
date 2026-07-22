# Makefile for clai

VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X main.version=$(VERSION)

.DEFAULT_GOAL := build

.PHONY: build install run test fmt vet check clean help

build:
	go build -ldflags "$(LDFLAGS)" -o clai ./cmd/clai

install:
	go install -ldflags "$(LDFLAGS)" ./cmd/clai

run: build
	./clai

test:
	go test ./...

fmt:
	gofmt -l -w .

vet:
	go vet ./...

check:
	@echo "==> gofmt"
	@unformatted="$$(gofmt -l .)"; \
	if [ -n "$$unformatted" ]; then \
		echo "gofmt found unformatted files:"; \
		echo "$$unformatted"; \
		exit 1; \
	fi
	@echo "==> go vet"
	go vet ./...
	@echo "==> go test"
	go test ./...

clean:
	rm -f clai

help:
	@echo "Targets:"
	@echo "  build    build ./clai with version ldflags (default)"
	@echo "  install  go install ./cmd/clai with version ldflags"
	@echo "  run      build then run ./clai"
	@echo "  test     go test ./..."
	@echo "  fmt      gofmt -l -w ."
	@echo "  vet      go vet ./..."
	@echo "  check    gofmt check, vet, and test (CI)"
	@echo "  clean    remove the clai binary"
	@echo ""
	@echo "VERSION = $(VERSION)"
