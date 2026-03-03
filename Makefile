VERSION ?= 0.1.0
LDFLAGS = -ldflags="-s -w -X main.version=$(VERSION)"

.PHONY: all build agent tunnel clean test dist release

all: build

build: agent tunnel

agent:
	go build $(LDFLAGS) -o muster-agent ./cmd/muster-agent

tunnel:
	go build $(LDFLAGS) -o muster-tunnel ./cmd/muster-tunnel

clean:
	rm -f muster-agent muster-tunnel
	rm -rf dist

test:
	go test ./...

PLATFORMS = linux/amd64 linux/arm64 darwin/amd64 darwin/arm64

# Cross-compilation targets
dist:
	@mkdir -p dist
	GOOS=linux GOARCH=amd64 go build $(LDFLAGS) -o dist/muster-agent-linux-amd64 ./cmd/muster-agent
	GOOS=linux GOARCH=arm64 go build $(LDFLAGS) -o dist/muster-agent-linux-arm64 ./cmd/muster-agent
	GOOS=darwin GOARCH=amd64 go build $(LDFLAGS) -o dist/muster-agent-darwin-amd64 ./cmd/muster-agent
	GOOS=darwin GOARCH=arm64 go build $(LDFLAGS) -o dist/muster-agent-darwin-arm64 ./cmd/muster-agent
	GOOS=linux GOARCH=amd64 go build $(LDFLAGS) -o dist/muster-tunnel-linux-amd64 ./cmd/muster-tunnel
	GOOS=linux GOARCH=arm64 go build $(LDFLAGS) -o dist/muster-tunnel-linux-arm64 ./cmd/muster-tunnel
	GOOS=darwin GOARCH=amd64 go build $(LDFLAGS) -o dist/muster-tunnel-darwin-amd64 ./cmd/muster-tunnel
	GOOS=darwin GOARCH=arm64 go build $(LDFLAGS) -o dist/muster-tunnel-darwin-arm64 ./cmd/muster-tunnel

# Build dist + generate checksums for GitHub release upload
release: clean dist
	cd dist && shasum -a 256 * > checksums.txt
	@echo ""
	@echo "Release artifacts (v$(VERSION)):"
	@ls -lh dist/
	@echo ""
	@echo "Upload with: gh release create v$(VERSION) dist/* --title v$(VERSION)"
