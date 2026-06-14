# zx_go — Make targets for common build/test/lint workflows.
#
# `make build` produces a clean `bin/zx_go` binary without the
# `ld: warning: ignoring duplicate libraries: '-lobjc'` cosmetic
# warning that Apple's newer linker emits when transitive
# dependencies (fyne + systray) each declare `#cgo LDFLAGS: -lobjc`.
# The duplicate libraries are silently merged by the linker; the
# warning is harmless. `-Wl,-no_warn_duplicate_libraries` opts
# out of it on darwin only. Linux / Windows builds skip the flag.
#
# Bare `go build ./cmd/zx_go` still works — these targets are a
# convenience, not a requirement.

GO       ?= go
BIN_DIR  ?= bin
BINARY   := $(BIN_DIR)/zx_go

# Stamp the build date into the binary (shown in the startup banner,
# Help → About, and --version). A bare `go build` without this still
# works — pkg/version falls back to the embedded VCS commit date.
VERSION_PKG := github.com/conorarmstrong/zx_go/pkg/version
# RFC 3339 UTC timestamp (space-free, so the linker's -X accepts it;
# pkg/version formats it as "YYYY-MM-DD HH:MM:SS UTC" for display).
BUILD_DATE  ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS     := -X $(VERSION_PKG).BuildDate=$(BUILD_DATE)

UNAME_S  := $(shell uname -s)
ifeq ($(UNAME_S),Darwin)
# macOS: silence the duplicate-libobjc linker warning fyne+systray emit.
LDFLAGS += -extldflags=-Wl,-no_warn_duplicate_libraries
endif

.PHONY: build test vet lint clean run all

all: build

build: $(BIN_DIR)
	$(GO) build -ldflags="$(LDFLAGS)" -o $(BINARY) ./cmd/zx_go

$(BIN_DIR):
	mkdir -p $(BIN_DIR)

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

lint:
	golangci-lint run

run: build
	$(BINARY)

clean:
	rm -rf $(BIN_DIR)
