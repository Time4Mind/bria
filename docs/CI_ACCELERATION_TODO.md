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
| CI5 | Full verification and standing-authorized release | complete | Final source `cfc093e`; full local gate, exact-SHA CI, install/restart and postflight pass |

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
  fixed. Final rereview approved it and the fresh full release gate passed.

## Release receipt

Final source `cfc093e1baa0d56fc2b51e3aec737746b2b3e060` is present in
`origin/main`. `VERSION=20260910-ci-acceleration make check-full` passed on the
exact tree, including all-package plain/race tests, vet, architecture, policy,
packaging and executable acceptance. Independent rereview approved the final
implementation; `actionlint v1.7.12` and YAML parsing also passed.

Exact-SHA Stage 1 run `34446639208` and Platform run `34446639207` passed.
Stage 1 completed in 2m31s versus 4m01s-4m25s before the change: classifier
16s, race worker 1m17s and standard worker 2m02s ran through the fail-closed
aggregate. Platform completed in 2m17s. The first cache pin emitted a Node 20
deprecation warning; the final source uses official Node 24-native
`actions/cache` v6.1.0 commit `55cc834...`, and the warning is absent.

Release `20260910-ci-acceleration` is current; previous points to
`20260910-rich-back-navigation`. The installed trio matches the checked files:
Bria `80ae22a8...6005a7`, Codex adapter `f9e24dbe...9a46ec`, Claude adapter
`54203c53...cdab3`. The first manual `mv -f` pointer attempt was caught by the
mandatory reread before restart: macOS followed the destination directory
symlink and left both managed pointers unchanged. The two exact temporary
links were relocated with the repository's supported `mv -fh` semantics; no
temporary link remains and no service/state mutation occurred before correction.

Postflight at 2026-09-10 09:52 Europe/Moscow: launchd is running with new PID
36052, and that sole PID holds `.state.json.lock`. Config/state compatibility
and Telegram identity pass. Seven sessions, two active sessions, seven cards,
553 history entries, 12 journal sessions and 26 inputs remain; config/settings
hashes are unchanged. Startup emitted `telegram.flow_ready`; one session was
recovered and one bounded startup recovery returned unknown while the stored
session population remains four archived, two ready and one running. No fresh
critical event appeared after readiness. No Telegram input or synthetic user
flow was sent.

This receipt is itself the acceptance probe for the lightweight route: only
this root `*_TODO.md` and `STATUS_AND_NEXT.md` change, so both workflows must
select `docs-only`, run the documentation gate/aggregates, and skip every heavy
runtime/platform worker.
