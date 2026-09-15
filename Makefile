GO ?= go
BINARY := carbonate

.PHONY: all build gui test race vet fmt fmtcheck cover clean

all: fmtcheck vet test build

build:
	$(GO) build -o $(BINARY) ./cmd/carbonate

# The GTK window is behind a build tag so that the daemon, the tests and CI
# need no C toolchain. Building it needs gtk4 and gobject-introspection
# headers; on an immutable host, a toolbox container is the way.
gui:
	CGO_ENABLED=1 $(GO) build -tags gtk -o $(BINARY)-gui ./cmd/carbonate-gui

test:
	$(GO) test ./...

# Requires a C compiler. The auth handler is called from request goroutines,
# so this is the run that matters for sessionStore.
race:
	CGO_ENABLED=1 $(GO) test -race -count=1 ./...

vet:
	$(GO) vet ./...

fmt:
	$(GO) fmt ./...

fmtcheck:
	@unformatted=$$(gofmt -l .); \
	if [ -n "$$unformatted" ]; then \
		echo "not gofmt'd:"; echo "$$unformatted"; exit 1; \
	fi

cover:
	$(GO) test -coverprofile=coverage.out ./...
	$(GO) tool cover -func=coverage.out | tail -1

clean:
	rm -f $(BINARY) $(BINARY)-gui coverage.out
