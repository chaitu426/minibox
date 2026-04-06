SHELL := /bin/bash

.PHONY: build install-user install-system run-daemon help

help:
	@echo "Targets:"
	@echo "  make build          - build local binaries into ./bin"
	@echo "  make install-user   - install minibox/miniboxd into ~/.local/bin"
	@echo "  make install-system - install minibox/miniboxd into /usr/local/bin"
	@echo "  make run-daemon     - run daemon with sudo using installed command"

build:
	@mkdir -p bin
	go build -o bin/minibox-cli ./cmd/cli
	go build -o bin/minibox-daemon ./cmd/daemon

install-user:
	@bash scripts/install-commands.sh --user

install-system:
	@bash scripts/install-commands.sh --system

run-daemon:
	@sudo -E miniboxd
