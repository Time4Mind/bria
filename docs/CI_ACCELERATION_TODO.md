# CI acceleration

## Task contract

- Result: shorten the path from completed Bria code to a verified release without
  removing any runtime, race, platform, packaging, policy, or service postflight
  check that applies to executable changes.
- Sources: the user request from 2026-09-10, `Makefile`, GitHub Actions workflows,
  repository policy tests, and observed GitHub run durations.
- Unit and coverage: one pushed change set. Markdown-only changes below `docs/`
  may use a bounded documentation gate; every other path is fail-closed to the
  complete runtime gate.
- Exclusions: no product-runtime behavior, secrets, external configuration,
  persisted state, or unrelated service is changed.
- Allowed writes: repository tests, CI workflows, Makefile targets, this durable
  task record, ordinary commit/push to `Time4Mind/bria`, and the standing-authorized
  restart of `gui/501/com.time4mind.bria.v2` after exact-SHA CI passes.
- Acceptance: classifier RED/GREEN; full runtime checks remain present and
  required for non-doc paths; documentation-only routing runs policy/link/filename
  checks; Go caches are enabled; Stage 1 non-race and race checks run in parallel;
  fresh local `make check-full`, exact-SHA remote workflows, matching installed
  artifacts, sole healthy service process/lock, and preserved user state.

## Coverage and todo

| ID | Requirement | Status | Evidence / remaining boundary |
|---|---|---|---|
| CI1 | Fail-closed docs-only classifier | verified locally | NUL input and allowlist RED/GREEN; real runtime and receipt commits classify `full`/`docs-only` |
| CI2 | Parallel Stage 1 without dropping checks | verified locally | Standard and race workers feed an always-running aggregate job; old checks mapped one-for-one |
| CI3 | Safe Go module/build cache | verified locally | Pinned `actions/cache`; keys include OS, arch, Go, dependencies and gate identity; Stage 1 adds source hash with bounded restore |
| CI4 | Skip heavy platform workflow only for release journals | verified locally | Both workflows use the same classifier; only `STATUS_AND_NEXT.md` and root `*_TODO.md` are light |
| CI5 | Full verification and standing-authorized release | in progress | Targeted/non-race gates and actionlint pass; final full gate, exact-SHA CI and postflight pending |

Integration owner: the main agent. Read-only reviewers own CI routing and
cache/parallelism analysis; only the integration owner edits and releases.

## Local evidence

- Classifier RED proved the missing allowlist behavior; a separate RED reproduced
  malformed non-NUL input being accepted. Both are GREEN after the bounded fix.
- `go test -mod=readonly ./scripts`, `packaging/test_contracts.sh`, YAML parsing,
  `actionlint v1.7.12`, and `VERSION=20260910-ci-acceleration
  make check-full-no-race` pass on the integrated tree.
- Historical runtime commit `72e6c0a` classifies `full`; release-receipt commit
  `5e9d5f5` classifies `docs-only`. Unknown base, empty input, mixed paths,
  controls, product docs, nested docs and non-Markdown docs classify `full`.
- Independent review found one malformed-input blocker; it was reproduced and
  fixed. Final rereview and one fresh full release gate remain pending.
