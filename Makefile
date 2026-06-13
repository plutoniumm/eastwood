BINARY   := eastwood
BUILD    := go build -o $(BINARY) .
BUILD_TS := go build -tags ts_svelte -o $(BINARY) .

SVELTE_GRAMMAR_REPO := https://github.com/Himujjal/tree-sitter-svelte
SVELTE_GRAMMAR_DIR  := language/svelte/grammar/src

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

## release VERSION=x.y.z: build macOS binaries in parallel, publish, update formula, tag+push
release:
	@if [ -z "$(VERSION)" ]; then echo "usage: make release VERSION=x.y.z"; exit 1; fi
	@echo "→ Building v$(VERSION) for macOS (arm64 + amd64) in parallel..."
	@rm -rf $(DIST) && mkdir -p $(DIST)
	@( \
	  ( CGO_ENABLED=1 GOOS=darwin GOARCH=arm64 \
	      go build -trimpath -ldflags="-s -w -X main.version=$(VERSION)" \
	      -o $(DIST)/eastwood_darwin_arm64 . \
	      && echo "  ✓ darwin/arm64" ) & P1=$$!; \
	  ( CGO_ENABLED=1 GOOS=darwin GOARCH=amd64 \
	      go build -trimpath -ldflags="-s -w -X main.version=$(VERSION)" \
	      -o $(DIST)/eastwood_darwin_amd64 . \
	      && echo "  ✓ darwin/amd64" ) & P2=$$!; \
	  RC=0; \
	  wait $$P1 || RC=1; \
	  wait $$P2 || RC=1; \
	  exit $$RC \
	)

	@echo "→ Creating tarballs..."
	@cd $(DIST) && for f in eastwood_*; do mv $$f eastwood && tar czf $$f.tar.gz eastwood && rm eastwood; done

	@echo "→ Computing checksums..."
	@cd $(DIST) && shasum -a 256 *.tar.gz > checksums.txt && cat checksums.txt

	@echo "→ Updating Formula/eastwood.rb..."
	@perl -i -pe 's/version ".*"/version "$(VERSION)"/' Formula/eastwood.rb
	@cd $(DIST) && \
	  SHA_DARWIN_ARM64=$$(grep darwin_arm64 checksums.txt | awk '{print $$1}'); \
	  SHA_DARWIN_AMD64=$$(grep darwin_amd64 checksums.txt | awk '{print $$1}'); \
	  cd .. && \
	  sed -i '' -e "/darwin_arm64\.tar\.gz/{n; s/sha256 \".*\"/sha256 \"$$SHA_DARWIN_ARM64\"/;}" Formula/eastwood.rb && \
	  sed -i '' -e "/darwin_amd64\.tar\.gz/{n; s/sha256 \".*\"/sha256 \"$$SHA_DARWIN_AMD64\"/;}" Formula/eastwood.rb

	@echo "→ Publishing to GitHub releases..."
	gh release create "v$(VERSION)" \
	  --repo $(REPO) \
	  --title "v$(VERSION)" \
	  --generate-notes \
	  $(DIST)/*.tar.gz $(DIST)/checksums.txt

	@echo "→ Committing and tagging..."
	git add Formula/eastwood.rb
	git diff --cached --quiet || git commit -m "release v$(VERSION)"
	git tag -f v$(VERSION)
	git push origin main
	git push --force origin v$(VERSION)

	@echo ""
	@echo "✓ Released v$(VERSION) at https://github.com/$(REPO)/releases/tag/v$(VERSION)"
	@echo "  Install with:"
	@echo "    brew tap plutoniumm/eastwood https://github.com/plutoniumm/eastwood"
	@echo "    brew install eastwood"
