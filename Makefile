.PHONY: all build test fmt install clean

GO ?= $(shell command -v go 2>/dev/null || echo /usr/local/go/bin/go)
BINARY := rig
OUTPUT ?= bin/$(BINARY)
INSTALL_DIR ?= $(HOME)/.local/bin

all: fmt test build

build:
	$(GO) build -trimpath -o $(OUTPUT) ./cmd/rig

test:
	$(GO) test -race -count=1 ./...

fmt:
	$(GO) fmt ./...

install: build
	install -d "$(INSTALL_DIR)"
	install -m 0755 "$(OUTPUT)" "$(INSTALL_DIR)/$(BINARY)"
	@echo "Installed $(BINARY) to $(INSTALL_DIR)/$(BINARY)"

clean:
	rm -rf bin/
