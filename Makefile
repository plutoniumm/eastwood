BINARY   := eastwood
BUILD    := go build -o $(BINARY) ./cmd/eastwood/...
BUILD_TS := go build -tags ts_svelte -o $(BINARY) ./cmd/eastwood/...

SVELTE_GRAMMAR_REPO := https://github.com/Himujjal/tree-sitter-svelte
SVELTE_GRAMMAR_DIR  := svelte/grammar/src

.PHONY: build build-svelte setup-svelte clean test lint-self

## build: compile the binary (Svelte uses text/regex rules only)
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

## clean: remove built binary and cached results
clean:
	rm -f $(BINARY)
	rm -rf ~/.cache/eastwood

## lint-self: lint this codebase with eastwood (requires build first)
lint-self: build
	./$(BINARY) .
