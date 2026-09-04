BINARY_GATEWAY := soulacy
BINARY_CLI     := sy
VERSION        ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS        := -ldflags "-X github.com/soulacy/soulacy/internal/config.Version=$(VERSION)"
PLAYWRIGHT_RUNNER ?= $(shell if [ -e .cache ] && [ ! -d .cache ]; then echo tmp/playwright-runner; else echo .cache/playwright-runner; fi)

.PHONY: all build build-gateway build-cli gui up install which test regression uat uat-public uat-full uat-credential docs-build docs-screenshots release-smoke production-parity channel-golden-smoke browser-mcp-smoke lint dev run-dev sdk-install tidy \
        docker-up docker-down docker-up-lite docker-build docker-push \
        release release-linux release-linux-amd64 release-linux-arm64 \
        release-darwin release-darwin-arm64 release-darwin-amd64 release-package release-create release-create-github \
        service-install service-uninstall deploy help

## Full build: unified GUI (Studio is built in as a first-class route), then
## Go binaries. ARCH-6 folded the Studio visual builder into the core GUI, so
## there is no longer a separate plugin-ui step — `make gui` embeds Studio.
all: gui build

# The embedded GUI bundle (go:embed all:dist) and the sources it's built from.
# Defined BEFORE the `build` rule so `$(GUI_DIST)` is non-empty when used as a
# prerequisite below (make expands a := variable at the point it is read).
GUI_DIST := internal/webui/dist/index.html
GUI_SRC  := $(shell find gui/src gui/index.html gui/package.json gui/package-lock.json \
              gui/vite.config.js gui/svelte.config.js gui/tailwind.config.js \
              gui/postcss.config.js gui/public 2>/dev/null)

## Build the binaries AND refresh the embedded GUI when its sources changed.
## The GUI step is incremental: $(GUI_DIST) only rebuilds when a file under
## gui/ is newer than the last build, so a pure-Go change skips npm entirely
## while a GUI change is always picked up. `go mod tidy` runs first so go.sum
## picks up any newly-added deps.
build: deps $(GUI_DIST) build-gateway build-cli

## Ensure module deps are fetched and go.sum is consistent.
deps:
	@echo "→ Ensuring go.mod/go.sum are in sync..."
	go mod tidy

## Incremental GUI build: only runs npm when a gui/ source is newer than the
## last embedded bundle. This is the dependency `build` uses.
$(GUI_DIST): $(GUI_SRC)
	@echo "→ GUI sources changed — rebuilding embedded GUI (Svelte)..."
	@command -v npm >/dev/null 2>&1 || { echo "npm not found — install Node.js from https://nodejs.org"; exit 1; }
	cd gui && npm install --silent && npm run build
	@touch $(GUI_DIST)
	@echo "→ GUI built."

## Force a full GUI rebuild regardless of timestamps (used by `make all`).
gui:
	@echo "→ Building GUI (Svelte)..."
	@command -v npm >/dev/null 2>&1 || { echo "npm not found — install Node.js from https://nodejs.org"; exit 1; }
	cd gui && npm install --silent && npm run build
	@echo "→ GUI built."

build-gateway:
	@echo "→ Building gateway server..."
	CGO_ENABLED=1 go build $(LDFLAGS) -o bin/$(BINARY_GATEWAY) ./cmd/soulacy

build-cli:
	@echo "→ Building CLI..."
	CGO_ENABLED=1 go build $(LDFLAGS) -o bin/$(BINARY_CLI) ./cmd/sy

## Install both binaries to the directory that ALREADY wins on your PATH, so an
## update always replaces the copy that actually runs (this avoids the classic
## "installed a new build but the old one keeps running" trap caused by having
## several soulacy copies in different PATH dirs). If soulacy isn't installed
## yet, defaults to ~/.local/bin (user-writable, no sudo). Override explicitly:
##     make install BINDIR=/usr/local/bin
BINDIR ?= $(shell d=$$(command -v $(BINARY_GATEWAY) 2>/dev/null); if [ -n "$$d" ]; then dirname "$$d"; else echo "$$HOME/.local/bin"; fi)
install: all
	@echo "→ Installing to $(BINDIR)..."
	@mkdir -p "$(BINDIR)"
	@install_bin() { \
	  src="$$1"; dst="$(BINDIR)/$$2"; tmp="$${dst}.tmp.$$PPID"; \
	  if [ -f "$$dst" ] && shasum -a 256 "$$src" "$$dst" 2>/dev/null | awk '{print $$1}' | uniq -d | grep -q .; then \
	    echo "• $$2 already current in $(BINDIR)"; \
	    return 0; \
	  fi; \
	  rm -f "$$tmp"; \
	  cp "$$src" "$$tmp"; \
	  chmod 0755 "$$tmp"; \
	  command -v xattr >/dev/null 2>&1 && xattr -c "$$tmp" 2>/dev/null || true; \
	  mv -f "$$tmp" "$$dst"; \
	}; \
	install_bin bin/$(BINARY_GATEWAY) $(BINARY_GATEWAY); \
	install_bin bin/$(BINARY_CLI) $(BINARY_CLI)
	@echo "✓ Installed soulacy and sy to $(BINDIR)"
	@# Shadow check: confirm the copy we just wrote is the one PATH resolves.
	@resolved=$$(command -v $(BINARY_GATEWAY) 2>/dev/null); \
	if [ "$$resolved" = "$(BINDIR)/$(BINARY_GATEWAY)" ]; then \
	  echo "✓ 'soulacy' on your PATH now points at this fresh build ($$resolved)."; \
	else \
	  echo ""; \
	  echo "⚠  Heads up: 'soulacy' still resolves to $$resolved (a different copy)."; \
	  echo "   Every soulacy found on your PATH:"; \
	  oldIFS=$$IFS; IFS=:; for d in $$PATH; do [ -x "$$d/$(BINARY_GATEWAY)" ] && echo "     $$d/$(BINARY_GATEWAY)"; done; IFS=$$oldIFS; \
	  echo "   Fix: delete the stale copy above, or re-run:  make install BINDIR=$$(dirname $$resolved)"; \
	fi

## Report which soulacy/sy your shell will actually run, and flag duplicates.
## Handy after an update: `make which`.
which:
	@resolved=$$(command -v $(BINARY_GATEWAY) 2>/dev/null); \
	echo "soulacy resolves to: $${resolved:-<not on PATH>}"; \
	echo "all copies on PATH:"; \
	oldIFS=$$IFS; IFS=:; for d in $$PATH; do [ -x "$$d/$(BINARY_GATEWAY)" ] && echo "  $$d/$(BINARY_GATEWAY)"; done; IFS=$$oldIFS; \
	echo "fresh build in repo: ./bin/$(BINARY_GATEWAY)"

## Upgrade in place: back up the currently installed binaries to *.prev, then
## build + install the fresh ones. Config and the workspace are untouched, so an
## upgrade is non-destructive and reversible via `make rollback`.
upgrade: all
	@echo "→ Upgrading soulacy in $(BINDIR) (backing up current build)..."
	@mkdir -p "$(BINDIR)"
	@[ -f "$(BINDIR)/$(BINARY_GATEWAY)" ] && cp "$(BINDIR)/$(BINARY_GATEWAY)" "$(BINDIR)/$(BINARY_GATEWAY).prev" || true
	@[ -f "$(BINDIR)/$(BINARY_CLI)" ] && cp "$(BINDIR)/$(BINARY_CLI)" "$(BINDIR)/$(BINARY_CLI).prev" || true
	@cp bin/$(BINARY_GATEWAY) "$(BINDIR)/$(BINARY_GATEWAY)"
	@cp bin/$(BINARY_CLI) "$(BINDIR)/$(BINARY_CLI)"
	@echo "✓ Upgraded. Previous build saved as *.prev — run 'make rollback' to revert."

## Rollback: restore the previous binaries saved by `make upgrade`.
rollback:
	@if [ ! -f "$(BINDIR)/$(BINARY_GATEWAY).prev" ]; then \
	  echo "✗ No previous build found in $(BINDIR). Nothing to roll back."; exit 1; \
	fi
	@echo "→ Rolling back soulacy in $(BINDIR)..."
	@cp "$(BINDIR)/$(BINARY_GATEWAY).prev" "$(BINDIR)/$(BINARY_GATEWAY)"
	@cp "$(BINDIR)/$(BINARY_CLI).prev" "$(BINDIR)/$(BINARY_CLI)"
	@echo "✓ Rolled back to the previous build."

## Health check the running install (provider/vault/channel diagnostics).
health:
	@$(BINARY_CLI) doctor || sy doctor

## Collect a redacted support/log bundle for troubleshooting or a bug report.
logs-bundle:
	@$(BINARY_CLI) support bundle || sy support bundle

## Optional real-channel golden smoke. Skips channels unless env targets are set.
## Example:
##   SOULACY_API_KEY=... SOULACY_GOLDEN_TELEGRAM_TO=123456 make channel-golden-smoke
channel-golden-smoke:
	@bash scripts/channel-golden-smoke.sh

## Optional Playwright MCP/browser sidecar smoke. Skips unless explicitly enabled.
## Example:
##   SOULACY_BROWSER_MCP_SMOKE=1 make browser-mcp-smoke
browser-mcp-smoke:
	@python3 scripts/browser-mcp-smoke.py

## Install Python SDK in editable mode (development)
sdk-install:
	pip3 install -e sdk/python

## Install Python SDK from PyPI
sdk-install-release:
	pip3 install soulacy

## ONE COMMAND: (re)build the GUI + binaries, then run the gateway serving the
## embedded GUI on http://localhost:18789. No vite dev server, no second process
## — what you see at :18789 is exactly the built binary. The GUI rebuild is
## incremental (only runs npm when gui/ changed). Stop any running gateway first
## (the port must be free). Override the config with:
##     make up CONFIG=./config.dev.yaml
CONFIG ?=
up: build
	@echo "→ Gateway on http://localhost:18789  (embedded GUI — Ctrl-C to stop)"
	@$(if $(CONFIG),SOULACY_CONFIG_PATH=$(CONFIG) ,)./bin/$(BINARY_GATEWAY) serve

## Run gateway in dev mode (auto-restart on changes with Air)
dev:
	@which air > /dev/null 2>&1 || go install github.com/cosmtrek/air@latest
	air -c .air.toml

## Build everything and serve with the repo dev config (config.dev.yaml).
## One-liner to see the portal incl. the Studio plugin during development.
run-dev: all
	@echo "→ Serving with ./config.dev.yaml — open http://127.0.0.1:18789"
	SOULACY_CONFIG_PATH=./config.dev.yaml ./bin/soulacy serve

## Run tests
test:
	go test ./... -v -timeout 30s

## Focused production smoke regression: core Go paths, GUI tests, GUI build.
regression:
	bash scripts/regression-smoke.sh

## Clean-workspace UAT: boots a separate runtime, exercises core APIs, then exits.
## `uat` stays as the historic no-secret alias (== uat-public). Use `uat-full`
## when TELEGRAM_*/SLACK_*/DISCORD_* tokens are exported and you want to
## actually deliver messages. Both variants generate a timestamped Markdown
## report under .cache/uat-reports/ by default (override with SOULACY_UAT_REPORT).
uat: uat-public

## Non-secret subset — safe for CI. Every credential-gated block is skipped
## via SOULACY_UAT_MODE=public regardless of what tokens happen to be set.
uat-public: build
	SOULACY_UAT_MODE=public bash scripts/uat-clean-runtime.sh

## Full subset — includes live channel delivery blocks when the matching env
## pairs are present. Each block still skips cleanly if only some pairs are set.
uat-full: build
	SOULACY_UAT_MODE=full bash scripts/uat-clean-runtime.sh

## Cohort E2 — credential-backed UAT harness. Loads `.env.uat` (or
## `scripts/.env.uat`; override with ENV_UAT=/path/.env.uat) and runs a real
## cloud-provider probe, a local-model probe, live channel delivery per
## configured platform (Telegram / Slack / Discord / email), a scheduled
## one-shot, and a Studio repair loop. Credentials NEVER leave the operator's
## machine — this target is intentionally not wired into CI.
uat-credential: build
	bash scripts/uat-credential-smoke.sh

## Build the public documentation site locally.
docs-build:
	@echo "→ Building docs site..."
	@python3 -c "import mkdocs" >/dev/null 2>&1 || { echo "mkdocs not found — install with: python3 -m pip install mkdocs-material"; exit 1; }
	@cp install.sh docs/install.sh
	python3 -m mkdocs build --strict

## Capture launch screenshots for the public docs using the production GUI bundle.
docs-screenshots: build
	@echo "→ Capturing docs screenshots..."
	@mkdir -p docs/assets/screenshots
	@if [ ! -d "$(PLAYWRIGHT_RUNNER)/node_modules/playwright" ]; then \
	  echo "→ Installing ephemeral Playwright runner into $(PLAYWRIGHT_RUNNER)..."; \
	  mkdir -p "$(PLAYWRIGHT_RUNNER)"; \
	  npm install --prefix "$(PLAYWRIGHT_RUNNER)" --silent playwright >/dev/null; \
	fi
	@echo "→ Ensuring Playwright Chromium browser is installed..."
	@"$(PLAYWRIGHT_RUNNER)/node_modules/.bin/playwright" install chromium
	SOULACY_PLAYWRIGHT_REQUIRE_FROM="$(CURDIR)/$(PLAYWRIGHT_RUNNER)" SOULACY_BROWSER_RENDER_OUT="$(CURDIR)/docs/assets/screenshots" node scripts/browser-render-smoke.mjs
	@echo "✓ Screenshot manifest: docs/assets/screenshots/manifest.json"

## Install-like release smoke: copies built binaries to a temp PATH and runs clean-runtime UAT.
release-smoke: build
	bash scripts/release-smoke.sh

## Production parity automation: CI checks, regression, clean UAT, release smoke,
## race/vulnerability checks, SDK/docs, optional live channels/browser/Studio.
production-parity:
	bash scripts/production-parity.sh

## Lint
lint:
	@which golangci-lint > /dev/null 2>&1 || curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b $(go env GOPATH)/bin
	golangci-lint run

## Tidy modules
tidy:
	go mod tidy

clean:
	rm -rf bin/

# ──────────────────────────────────────────────────────────────────────────────
# Docker Compose — one-command full-stack deployment
# ──────────────────────────────────────────────────────────────────────────────

DOCKER         ?= docker
COMPOSE        ?= docker compose

## Start full stack (gateway + Postgres + Qdrant) — first run builds the image
docker-up:
	@echo "→ Starting full stack (gateway + Postgres + Qdrant)..."
	$(COMPOSE) up --build -d
	@echo "✓ Soulacy running at http://localhost:18789"

## Start lightweight stack (gateway + SQLite only, no external dependencies)
docker-up-lite:
	@echo "→ Starting lite stack (gateway + SQLite only)..."
	$(COMPOSE) -f docker-compose.lite.yml up --build -d
	@echo "✓ Soulacy running at http://localhost:18789"

## Stop all compose services
docker-down:
	$(COMPOSE) down

## Build the Docker image without starting
docker-build:
	$(COMPOSE) build

## Push image to registry (set REGISTRY env var)
docker-push:
	$(COMPOSE) push

# ──────────────────────────────────────────────────────────────────────────────
# System service — register soulacy as an OS service (Mac LaunchAgent / Linux systemd)
# ──────────────────────────────────────────────────────────────────────────────

OS := $(shell uname -s)
REAL_USER := $(if $(SUDO_USER),$(SUDO_USER),$(USER))
REAL_UID  := $(shell id -u $(REAL_USER))
REAL_HOME := $(if $(SUDO_USER),$(shell bash -c "eval echo ~$(SUDO_USER)"),$(HOME))
LAUNCHCTL := $(if $(SUDO_USER),sudo -u $(SUDO_USER) launchctl,launchctl)
SUDO_CMD  := $(if $(SUDO_USER),sudo -u $(SUDO_USER) ,)

## Install soulacy as a system service (starts on login/boot)
service-install: install
	@echo "→ Installing user-scoped Soulacy service..."
	@"$(BINDIR)/$(BINARY_CLI)" daemon install

## Remove soulacy system service
service-uninstall:
	@echo "→ Removing user-scoped Soulacy service..."
	@$(BINARY_CLI) daemon uninstall

## Build, install, and restart the gateway service
deploy: install
	@echo "→ Restarting user-scoped Soulacy service..."
	@$(BINARY_CLI) daemon stop || true
	@$(BINARY_CLI) daemon start

# ──────────────────────────────────────────────────────────────────────────────
# Release pipeline — produces platform-tagged binaries under bin/release/.
# Linux targets build inside a Docker container so cgo+sqlite-vec doesn't
# need a cross-toolchain on the host. Darwin targets build natively.
# ──────────────────────────────────────────────────────────────────────────────

RELEASE_DIR    := bin/release
RELEASE_IMAGE  := soulacy-release
DOCKER_BUILDKIT_FLAGS := --load

## Build every supported target (Linux amd64+arm64). Skip Darwin here;
## it requires a macOS host — use `make release-darwin` on macOS.
release: release-linux release-package

## All Linux targets.
release-linux: release-linux-amd64 release-linux-arm64

release-linux-amd64:
	@echo "→ Release: linux/amd64 (cgo, sqlite-vec) via Dockerfile.release"
	@mkdir -p $(RELEASE_DIR)
	DOCKER_BUILDKIT=1 $(DOCKER) buildx build \
		--platform linux/amd64 \
		--build-arg VERSION=$(VERSION) \
		-f Dockerfile.release \
		-t $(RELEASE_IMAGE):linux-amd64 \
		$(DOCKER_BUILDKIT_FLAGS) .
	@$(DOCKER) rm -f soulacy-extract-amd64 >/dev/null 2>&1 || true
	$(DOCKER) create --name soulacy-extract-amd64 $(RELEASE_IMAGE):linux-amd64 >/dev/null
	$(DOCKER) cp soulacy-extract-amd64:/out/soulacy $(RELEASE_DIR)/soulacy-linux-amd64
	$(DOCKER) cp soulacy-extract-amd64:/out/sy      $(RELEASE_DIR)/sy-linux-amd64
	$(DOCKER) rm soulacy-extract-amd64 >/dev/null
	@echo "✓ $(RELEASE_DIR)/soulacy-linux-amd64"

release-linux-arm64:
	@echo "→ Release: linux/arm64 (cgo, sqlite-vec) via Dockerfile.release"
	@mkdir -p $(RELEASE_DIR)
	DOCKER_BUILDKIT=1 $(DOCKER) buildx build \
		--platform linux/arm64 \
		--build-arg VERSION=$(VERSION) \
		-f Dockerfile.release \
		-t $(RELEASE_IMAGE):linux-arm64 \
		$(DOCKER_BUILDKIT_FLAGS) .
	@$(DOCKER) rm -f soulacy-extract-arm64 >/dev/null 2>&1 || true
	$(DOCKER) create --name soulacy-extract-arm64 $(RELEASE_IMAGE):linux-arm64 >/dev/null
	$(DOCKER) cp soulacy-extract-arm64:/out/soulacy $(RELEASE_DIR)/soulacy-linux-arm64
	$(DOCKER) cp soulacy-extract-arm64:/out/sy      $(RELEASE_DIR)/sy-linux-arm64
	$(DOCKER) rm soulacy-extract-arm64 >/dev/null
	@echo "✓ $(RELEASE_DIR)/soulacy-linux-arm64"

## Darwin targets — built natively; macOS + Xcode required.
release-darwin: release-darwin-arm64 release-package

release-darwin-arm64:
	@echo "→ Release: darwin/arm64 (native cgo build)"
	@mkdir -p $(RELEASE_DIR)
	CGO_ENABLED=1 GOOS=darwin GOARCH=arm64 \
		go build $(LDFLAGS) -o $(RELEASE_DIR)/soulacy-darwin-arm64 ./cmd/soulacy
	CGO_ENABLED=1 GOOS=darwin GOARCH=arm64 \
		go build $(LDFLAGS) -o $(RELEASE_DIR)/sy-darwin-arm64 ./cmd/sy
	@echo "✓ $(RELEASE_DIR)/soulacy-darwin-arm64"

release-darwin-amd64:
	@echo "→ Release: darwin/amd64 (native cgo build)"
	@mkdir -p $(RELEASE_DIR)
	CGO_ENABLED=1 GOOS=darwin GOARCH=amd64 \
		go build $(LDFLAGS) -o $(RELEASE_DIR)/soulacy-darwin-amd64 ./cmd/soulacy
	CGO_ENABLED=1 GOOS=darwin GOARCH=amd64 \
		go build $(LDFLAGS) -o $(RELEASE_DIR)/sy-darwin-amd64 ./cmd/sy
	@echo "✓ $(RELEASE_DIR)/soulacy-darwin-amd64"

## Package release binaries into GitHub-release tarballs plus checksums.
release-package:
	VERSION=$(VERSION) RELEASE_DIR=$(RELEASE_DIR) bash scripts/package-release.sh

## Create a release tag from origin/main and push it to trigger GitHub Actions.
release-create:
	VERSION=$(VERSION) bash scripts/create-release.sh

## Ask GitHub Actions to create the release tag, which triggers the release workflow.
release-create-github:
	@if ! printf '%s\n' "$(VERSION)" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$$'; then \
	  echo "VERSION must look like v1.2.3 or v1.2.3-rc.1, e.g. make release-create-github VERSION=v0.1.0"; exit 2; \
	fi
	gh workflow run create-release.yml -f version=$(VERSION) -f target_ref=$(or $(RELEASE_REF),main) -f dry_run=false

## Show help
help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/## //' | column -t -s '—'
	@echo ""
	@echo "Quick start:"
	@echo "  make all              Build GUI + binaries locally"
	@echo "  make install          Install to /usr/local/bin"
	@echo "  make docker-up        Start full stack in Docker"
	@echo "  make docker-up-lite   Start SQLite-only stack in Docker"
	@echo "  make service-install  Register as OS service (auto-start)"
	@echo "  make upgrade          Rebuild + install, backing up the prior build"
	@echo "  make rollback         Restore the previous build after an upgrade"
	@echo "  make health           Run diagnostics (sy doctor)"
	@echo "  make logs-bundle      Collect a redacted support bundle"
	@echo "  make release-create VERSION=v0.1.0"
	@echo "                        Tag origin/main and start the GitHub release workflow"
	@echo "  make release-create-github VERSION=v0.1.0"
	@echo "                        Ask GitHub Actions to create the release tag"
