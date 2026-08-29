BINARY := limitping
MODULE := github.com/ShawnKung/limitping
PREFIX ?= $(HOME)/.local
BINDIR ?= $(PREFIX)/bin
DESTDIR ?=
UNAME_S := $(shell uname -s)
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
VERSION_LDFLAG := -X $(MODULE)/internal/cli.version=$(VERSION)

ifeq ($(UNAME_S),Darwin)
BUILD_LDFLAGS := -linkmode=external -s -w $(VERSION_LDFLAG)
TEST_FLAGS := -ldflags=-linkmode=external
SIGN_BINARY = if command -v codesign >/dev/null 2>&1; then codesign --force --sign - bin/$(BINARY); fi
else
BUILD_LDFLAGS := -s -w $(VERSION_LDFLAG)
TEST_FLAGS :=
SIGN_BINARY = true
endif

.PHONY: all build install uninstall test vet fmt check clean

all: build

build:
	mkdir -p bin
	go build -trimpath -ldflags '$(BUILD_LDFLAGS)' -o bin/$(BINARY) ./cmd/limitping
	$(SIGN_BINARY)

install: build
	install -d "$(DESTDIR)$(BINDIR)"
	install -m 0755 bin/$(BINARY) "$(DESTDIR)$(BINDIR)/$(BINARY)"

uninstall:
	rm -f "$(DESTDIR)$(BINDIR)/$(BINARY)"

test:
	go test $(TEST_FLAGS) ./...

vet:
	go vet ./...

fmt:
	gofmt -w ./cmd ./internal

check:
	test -z "$$(gofmt -l ./cmd ./internal)"
	go vet ./...
	go test $(TEST_FLAGS) ./...

clean:
	rm -f bin/$(BINARY)
