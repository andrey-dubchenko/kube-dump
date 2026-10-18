# kube-dump build script.
#
#   make            build for this machine
#   make windows    dist/windows-amd64/kube-dump.exe (the icon is linked in from the .syso)
#   make linux      dist/linux-amd64/: binary, desktop entry, icons, install.sh
#   make dist       both of the above
#   make check      gofmt + go vet + tests
#   make install    (Linux) build for Linux and install into ~/.local (PREFIX=/usr/local for system-wide)
#   make icons      redraw assets/icon.ico and the PNG icons
#   make resources  recompile the Windows resources (needs windres from MinGW)
#   make clean
#   make VERSION=1.2.0 build     set the version printed by `kube-dump -version`
#
# On Windows every target is handed to make.ps1: cmd.exe has none of the POSIX tools
# (mkdir -p, cp, rm, sh) that the Linux/macOS rules below use.

.DEFAULT_GOAL := build
TARGETS := build windows linux dist test vet fmt check icons resources clean help

ifeq ($(OS),Windows_NT)

.PHONY: all install $(TARGETS)

all: build

$(TARGETS):
	powershell -NoProfile -ExecutionPolicy Bypass -File make.ps1 $@ $(if $(VERSION),-Version $(VERSION))

install:
	@echo install is for Linux: build with 'make linux', copy dist/linux-amd64 to the machine and run install.sh

else

GO      ?= go
NAME    := kube-dump
PKG     := ./cmd/kube-dump
DIST    := dist
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
BUILD   := $(GO) build -trimpath -ldflags "$(LDFLAGS)"

.PHONY: all install $(TARGETS)

all: build

build:
	$(BUILD) -o $(NAME) $(PKG)

windows: export GOOS := windows
windows: export GOARCH := amd64
windows:
	mkdir -p $(DIST)/windows-amd64
	$(BUILD) -o $(DIST)/windows-amd64/$(NAME).exe $(PKG)
	cp configuration.example.yml $(DIST)/windows-amd64/

linux: export GOOS := linux
linux: export GOARCH := amd64
linux:
	mkdir -p $(DIST)/linux-amd64
	$(BUILD) -o $(DIST)/linux-amd64/$(NAME) $(PKG)
	rm -rf $(DIST)/linux-amd64/icons
	cp -r assets/icons $(DIST)/linux-amd64/icons
	cp packaging/linux/kube-dump.desktop packaging/linux/install.sh configuration.example.yml $(DIST)/linux-amd64/
	chmod +x $(DIST)/linux-amd64/$(NAME) $(DIST)/linux-amd64/install.sh

dist: windows linux

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

fmt:
	@out="$$(gofmt -l .)"; if [ -n "$$out" ]; then echo "gofmt needs to be run on:"; echo "$$out"; exit 1; fi

check: fmt vet test

install: linux
	sh $(DIST)/linux-amd64/install.sh

icons:
	$(GO) run assets/genicon.go

resources:
	cd assets && windres --use-temp-file -O coff -i kube-dump.rc -o ../cmd/kube-dump/resource_windows_amd64.syso

clean:
	rm -rf $(DIST) $(NAME) $(NAME).exe

help:
	@sed -n '2,13p' Makefile

endif
