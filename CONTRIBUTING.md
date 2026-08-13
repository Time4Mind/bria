# Contributing to Bria

Bria follows the Go specification, the standard library conventions, Effective
Go, the Go Code Review Comments, and the project rules below. These are
engineering constraints, not a style contest: when two guidelines conflict,
prefer the option that makes ownership, failure, and security easier to prove.

## Required checks

Every change must keep these commands clean:

```bash
go test -race ./...
go vet ./...
test -z "$(gofmt -l cmd internal)"
uv run python scripts/check_architecture.py
uv run python scripts/check_module_size.py --root . --suffix .go
```

Cross-platform code must also build for the targets listed in CI. A platform
build is not evidence that a platform-specific runtime path works; such paths
need tests on the target before they are described as supported.

## Go engineering rules

- Keep dependencies pointing inward: transport and platform adapters depend on
  application/domain contracts, never the reverse.
- Define small interfaces at the point where they are consumed. Return concrete
  types unless callers need substitution.
- Pass request-scoped `context.Context` as the first argument of blocking
  operations. A component that explicitly owns background workers may retain
  its own lifecycle context and cancel function; do not retain caller contexts.
- Wrap errors with useful operation context and preserve causes with `%w`.
  Compare expected errors with `errors.Is` or `errors.As`.
- Every goroutine needs an owner, a cancellation path, and a testable shutdown.
  Channels have one clearly documented closer.
- Protect shared mutable state explicitly and keep lock scope small. Code must
  remain correct under the race detector.
- Prefer zero values that are safe. Constructors validate dependencies and
  invariants that a zero value cannot satisfy.
- Use table-driven tests for state machines, parsers, authorization matrices,
  and protocol edge cases. Security and failover fixes require regression
  tests.
- Keep modules cohesive and reviewable. Split by responsibility before a file
  becomes a collection of unrelated flows; do not split merely to satisfy a
  line-count aesthetic.
- Never construct shell command strings from user or replicated data. Invoke
  executables with argument arrays and validate identifiers at trust
  boundaries.
- Persist before acknowledging operations whose replay could duplicate input
  or lifecycle effects. Retry paths must be idempotent.
- Use comments to explain invariants, concurrency, security boundaries, and
  non-obvious trade-offs. Do not narrate straightforward code.

Generated code, narrow platform shims, and well-contained protocol adapters may
depart from these defaults when doing so is clearer. Document the reason next
to the exception and keep the observable contract tested.
