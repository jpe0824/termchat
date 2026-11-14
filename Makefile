export CGO_ENABLED = 0

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X github.com/jpe0824/termchat/internal/cli.Version=$(VERSION)
GOBIN   ?= $(HOME)/go/bin

.PHONY: test build lint install release-snapshot

test:
	CGO_ENABLED=0 go test ./...

build:
	go build -ldflags "$(LDFLAGS)" -o termchat ./cmd/termchat

lint:
	go vet ./...

# install puts the binary in $(GOBIN), or ~/go/bin when GOBIN is unset.
install:
	go build -ldflags "$(LDFLAGS)" -o "$(GOBIN)/termchat" ./cmd/termchat

# release-snapshot builds every release archive into dist/ without publishing.
release-snapshot:
	goreleaser release --snapshot --clean --skip=publish
