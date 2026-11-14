export CGO_ENABLED = 0

.PHONY: test build lint

test:
	CGO_ENABLED=0 go test ./...

build:
	go build -o termchat ./cmd/termchat

lint:
	go vet ./...
