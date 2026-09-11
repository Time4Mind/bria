GO ?= go
GOFMT ?= gofmt
VERSION ?= dev
DIST_DIR ?= $(CURDIR)/dist
SOURCE_DATE_EPOCH ?= 0
REVISION ?=
RELEASE_KEY_ID ?=
RELEASE_SIGNING_KEY_FILE ?=
RELEASE_TRUST_FILE ?=
# Keep normal invocations independent of an ambient user-level Go cache.
# Command-line assignments still override these paths when a dedicated cache is
# useful for an isolated run.
GOCACHE := $(CURDIR)/.cache/go-build
GOMODCACHE := $(CURDIR)/.cache/go-mod
export GOCACHE GOMODCACHE
export VERSION DIST_DIR SOURCE_DATE_EPOCH REVISION RELEASE_KEY_ID RELEASE_SIGNING_KEY_FILE RELEASE_TRUST_FILE GO

GO_ENV = env GOENV=off GOTOOLCHAIN=local GOWORK=off GOPROXY=off GOSUMDB=off \
	GOCACHE="$(GOCACHE)" \
	GOMODCACHE="$(GOMODCACHE)" \
	CGO_ENABLED=0
GO_RACE_ENV = $(GO_ENV) CGO_ENABLED=1 GORACE=atexit_sleep_ms=0

.PHONY: prepare-deps check check-policy check-format check-architecture check-test check-race check-vet check-operational check-executable check-full-no-race check-full build release release-evidence release-supply-chain release-verify

# Explicit online preparation. All check/build targets remain offline and use
# the same cache; go.sum verification is never disabled during acquisition.
prepare-deps:
	$(GO_ENV) GOPROXY=https://proxy.golang.org,direct GOSUMDB=sum.golang.org $(GO) mod download
	$(GO_ENV) $(GO) mod verify

check: check-policy check-format check-architecture check-test check-vet check-operational

check-policy:
	$(GO_ENV) $(GO) run -mod=readonly ./scripts policy
	$(GO_ENV) $(GO) run -mod=readonly ./scripts links
	$(GO_ENV) $(GO) run -mod=readonly ./scripts filenames

check-format:
	@diff="$$(git ls-files -z --cached --others --exclude-standard -- '*.go' | \
		xargs -0 -r sh -c 'for file do test ! -f "$$file" || printf "%s\0" "$$file"; done' sh | \
		xargs -0 -r $(GO_ENV) $(GOFMT) -d)"; \
	if test -n "$$diff"; then \
		printf '%s\n' "$$diff"; \
		exit 1; \
	fi
	@printf '%s\n' 'Go formatting: OK'

check-architecture:
	$(GO_ENV) $(GO) run -mod=readonly ./scripts architecture
	$(GO_ENV) $(GO) test -mod=readonly ./scripts

check-test:
	$(GO_ENV) $(GO) test -mod=readonly ./...

check-race:
	$(GO_RACE_ENV) $(GO) test -race -mod=readonly ./...

check-vet:
	$(GO_ENV) $(GO) vet -mod=readonly ./...

check-operational:
	./packaging/verify-supply-chain.sh
	./packaging/test_contracts.sh
	$(GO_ENV) $(GO) test -mod=readonly ./packaging
	$(GO_ENV) $(GO) test -mod=readonly ./packaging/releasepack
	$(GO_ENV) $(GO) test -mod=readonly ./packaging/releasemanifest

build:
	@./packaging/build-local.sh
	@test -x bin/bria -a -x bin/bria-codex-adapter -a -x bin/bria-claude-adapter

check-executable: build
	@help="$$(./bin/bria --help)"; \
	case "$$help" in \
		*'Usage:'*) ;; \
		*) printf '%s\n' 'ERROR: bria --help returned unexpected output' >&2; exit 1 ;; \
	esac; \
	case "$$help" in \
		*'Owner-only private commands:'*'/menu'*'/model'*'/effort'*) ;; \
		*) printf '%s\n' 'ERROR: bria --help does not describe the supported runtime flow' >&2; exit 1 ;; \
	esac; \
	case "$$help" in \
		*'/new'*) printf '%s\n' 'ERROR: bria --help exposes unsupported /new command' >&2; exit 1 ;; \
		*) ;; \
	esac; \
	case "$$help" in \
		*'bria run --config /absolute/path/to/config.json'*'bria check-config --config /absolute/path/to/config.json'*'bria check-telegram --config /absolute/path/to/config.json'*) ;; \
		*) printf '%s\n' 'ERROR: bria --help does not document the runtime and preflight commands' >&2; exit 1 ;; \
	esac
	@set +e; unknown="$$(./bin/bria definitely-unknown-sensitive-sentinel 2>&1)"; exit_code=$$?; set -e; \
	test "$$exit_code" -eq 2 || { \
		printf 'ERROR: unknown command exit code = %s, want 2\n' "$$exit_code" >&2; \
		exit 1; \
	}; \
	test "$$unknown" = 'bria: unknown command' || { \
		printf '%s\n' 'ERROR: unknown command output is not generic' >&2; \
		exit 1; \
	}
	@printf '%s\n' 'Bria executable trio acceptance: OK'

check-full-no-race: check check-executable

check-full: check check-race check-executable

release:
	@./packaging/verify-supply-chain.sh
	@./packaging/build-release.sh
	@BRIA_RELEASE_MANIFEST="$(DIST_DIR)/$(VERSION)/release-manifest.json" $(GO_ENV) $(GO) run -mod=readonly ./scripts release-evidence

release-evidence:
	@BRIA_RELEASE_MANIFEST="$(DIST_DIR)/$(VERSION)/release-manifest.json" $(GO_ENV) $(GO) run -mod=readonly ./scripts release-evidence

release-supply-chain:
	@./packaging/verify-supply-chain.sh

release-verify:
	@./packaging/verify-release.sh
