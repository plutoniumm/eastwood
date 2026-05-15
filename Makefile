BINARY   := eastwood
BUILD    := go build -o $(BINARY) ./cmd/eastwood/...
BUILD_TS := go build -tags ts_svelte -o $(BINARY) ./cmd/eastwood/...

SVELTE_GRAMMAR_REPO := https://github.com/Himujjal/tree-sitter-svelte
SVELTE_GRAMMAR_DIR  := svelte/grammar/src

REPO     := plutoniumm/eastwood
DIST     := dist

.PHONY: build build-svelte setup-svelte clean test lint-self release

## build: compile the binary for the current platform
build:
	$(BUILD)

## build-svelte: compile with full tree-sitter-svelte support (run setup-svelte first)
build-svelte:
	$(BUILD_TS)

## setup-svelte: download tree-sitter-svelte grammar C sources
setup-svelte:
	@echo "→ Fetching tree-sitter-svelte grammar sources..."
	@mkdir -p $(SVELTE_GRAMMAR_DIR)
	@if command -v curl >/dev/null 2>&1; then \
		curl -fsSL "$(SVELTE_GRAMMAR_REPO)/raw/master/src/parser.c"  -o $(SVELTE_GRAMMAR_DIR)/parser.c && \
		curl -fsSL "$(SVELTE_GRAMMAR_REPO)/raw/master/src/scanner.c" -o $(SVELTE_GRAMMAR_DIR)/scanner.c 2>/dev/null || true; \
	elif command -v wget >/dev/null 2>&1; then \
		wget -q "$(SVELTE_GRAMMAR_REPO)/raw/master/src/parser.c"  -O $(SVELTE_GRAMMAR_DIR)/parser.c && \
		wget -q "$(SVELTE_GRAMMAR_REPO)/raw/master/src/scanner.c" -O $(SVELTE_GRAMMAR_DIR)/scanner.c 2>/dev/null || true; \
	else \
		echo "Error: curl or wget required"; exit 1; \
	fi
	@echo "✓ Grammar sources downloaded to $(SVELTE_GRAMMAR_DIR)/"
	@echo "  Now run: make build-svelte"

## test: run all tests
test:
	go test ./...

## clean: remove built binary, dist/, and cached results
clean:
	rm -f $(BINARY)
	rm -rf $(DIST)
	rm -rf ~/.cache/eastwood

## lint-self: lint this codebase with eastwood (requires build first)
lint-self: build
	./$(BINARY) .

## release VERSION=x.y.z: cross-compile, publish to manav.ch, update formula, commit+tag+push
release:
	@if [ -z "$(VERSION)" ]; then echo "usage: make release VERSION=x.y.z"; exit 1; fi
	@command -v zig >/dev/null 2>&1 || (echo "error: zig not found — brew install zig"; exit 1)
	@echo "→ Building v$(VERSION) for all platforms..."
	@rm -rf $(DIST) && mkdir -p $(DIST)

	@# darwin/arm64 — native clang on Apple Silicon
	CGO_ENABLED=1 GOOS=darwin GOARCH=arm64 \
	  go build -trimpath -ldflags="-s -w -X main.version=$(VERSION)" \
	  -o $(DIST)/eastwood_darwin_arm64 ./cmd/eastwood

	@# darwin/amd64 — macOS clang supports -arch x86_64 on arm64 runners
	CGO_ENABLED=1 GOOS=darwin GOARCH=amd64 \
	  go build -trimpath -ldflags="-s -w -X main.version=$(VERSION)" \
	  -o $(DIST)/eastwood_darwin_amd64 ./cmd/eastwood

	@# linux/amd64 — zig cc avoids glibc cross-compile dance
	CGO_ENABLED=1 GOOS=linux GOARCH=amd64 \
	  CC="zig cc -target x86_64-linux-musl" \
	  CXX="zig c++ -target x86_64-linux-musl" \
	  go build -trimpath -ldflags="-s -w -X main.version=$(VERSION)" \
	  -o $(DIST)/eastwood_linux_amd64 ./cmd/eastwood

	@# linux/arm64
	CGO_ENABLED=1 GOOS=linux GOARCH=arm64 \
	  CC="zig cc -target aarch64-linux-musl" \
	  CXX="zig c++ -target aarch64-linux-musl" \
	  go build -trimpath -ldflags="-s -w -X main.version=$(VERSION)" \
	  -o $(DIST)/eastwood_linux_arm64 ./cmd/eastwood

	@echo "→ Creating tarballs..."
	@cd $(DIST) && for f in eastwood_*; do tar czf $$f.tar.gz $$f && rm $$f; done

	@echo "→ Computing checksums..."
	@cd $(DIST) && shasum -a 256 *.tar.gz > checksums.txt && cat checksums.txt

	@echo "→ Updating Formula/eastwood.rb..."
	@perl -i -pe 's/version ".*"/version "$(VERSION)"/' Formula/eastwood.rb
	@cd $(DIST) && \
	  SHA_DARWIN_ARM64=$$(grep darwin_arm64 checksums.txt | awk '{print $$1}'); \
	  SHA_DARWIN_AMD64=$$(grep darwin_amd64 checksums.txt | awk '{print $$1}'); \
	  SHA_LINUX_ARM64=$$(grep  linux_arm64  checksums.txt | awk '{print $$1}'); \
	  SHA_LINUX_AMD64=$$(grep  linux_amd64  checksums.txt | awk '{print $$1}'); \
	  cd .. && \
	  perl -i -pe "s|PLACEHOLDER_darwin_arm64|$$SHA_DARWIN_ARM64|" Formula/eastwood.rb && \
	  perl -i -pe "s|PLACEHOLDER_darwin_amd64|$$SHA_DARWIN_AMD64|" Formula/eastwood.rb && \
	  perl -i -pe "s|PLACEHOLDER_linux_arm64|$$SHA_LINUX_ARM64|"   Formula/eastwood.rb && \
	  perl -i -pe "s|PLACEHOLDER_linux_amd64|$$SHA_LINUX_AMD64|"   Formula/eastwood.rb

	@echo "→ Publishing to GitHub releases..."
	gh release create "v$(VERSION)" \
	  --repo $(REPO) \
	  --title "v$(VERSION)" \
	  --generate-notes \
	  $(DIST)/*.tar.gz $(DIST)/checksums.txt

	@echo "→ Committing and tagging..."
	git add Formula/eastwood.rb
	git diff --cached --quiet || git commit -m "release v$(VERSION)"
	git tag v$(VERSION)
	git push origin main
	git push origin v$(VERSION)

	@echo ""
	@echo "✓ Released v$(VERSION) at https://github.com/$(REPO)/releases/tag/v$(VERSION)"
	@echo "  Install with:"
	@echo "    brew tap plutoniumm/eastwood https://github.com/plutoniumm/eastwood"
	@echo "    brew install eastwood"
