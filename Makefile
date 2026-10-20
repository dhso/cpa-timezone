PLUGIN := codex-timezone
# Empty keeps the default compiled into main.version; releases pass the tag without the leading v.
VERSION ?=

ifeq ($(OS),Windows_NT)
EXT := dll
else ifeq ($(shell uname -s),Darwin)
EXT := dylib
else
EXT := so
endif

.PHONY: build test check clean

build:
	go build -buildmode=c-shared $(if $(VERSION),-ldflags "-X main.version=$(VERSION)") -o bin/$(PLUGIN).$(EXT) .
	rm -f bin/$(PLUGIN).h

test:
	go test ./...

check:
	go vet ./...
	go test ./...

clean:
	rm -rf bin
