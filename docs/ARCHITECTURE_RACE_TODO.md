# Architecture and race fixes - 2026-09-08

## Immutable task contract

Owner: main agent. Source: current repository and Artem's requests to fix the
reported architecture/race defects, then push Git and restart the service.
Scope: three reproduced controller test race groups, three forbidden dependency
edges, ten oversized packages. Preserve current approval/configuration changes,
early durable acceptance semantics, package budgets and runtime behavior.
No legacy repository comparison. Timezone: Europe/Moscow; no data population.
Unit of acceptance: each defect plus integrated consumer regression checks.
Local source/test/docs/build writes authorized. Push/restart remain gated on
exact targets and the project's bounded-sequence confirmation rules. Never
touch unrelated bots, credentials or user changes.

## Coverage and ownership

| ID | Owner / exclusive write scope | Acceptance | State |
| --- | --- | --- | --- |
| A1 | Controller worker: internal/telegramcontroller; new controller helper package | Three race reproductions green repeatedly; controller <=5500 lines | done; independent 20-repeat checks |
| A2 | Screen worker: internal/screen, internal/screenproduction; new native render/cache packages | Screen <=750; composition <=200; behavior tests | done; independent race checks |
| A3 | Media worker: internal/mediaproduction, internal/p4runtimecomposition; new document adapter package | No telegram/controller import in media production; <=800; integration tests | done; reviewed custody behavior |
| A4 | Settings worker: internal/settings, internal/settingscomposition; new preference/codec package | Settings <=800; composition <=200; persisted toggle compatibility | done; migrations v1-v5 verified |
| A5 | Storage/transport worker: internal/storage, internal/telegram; new pure history/format packages | Storage <=1900; telegram <=1750; persistence/format tests | done; atomic commit boundary retained |
| A6 | Bridge worker: internal/telegrambridge; new callback helper package | Bridge <=1550; callback compatibility tests | done; signed callback consumer tests |
| A7 | Main: internal/nativeadapter, new internal/nativecapture, scripts | Remove screen dependency; adapter <=850; architecture green | done; real isolated tmux race tests |
| A8 | Main integration: docs, whole-tree verification/build | Full tests/race, policy, architecture, format, vet, physical binaries | done; full gate also passed after sanitization |
| A9 | Main: read-only Git/service target discovery, then gated release | Exact commit/remote branch reread; restart health/process evidence | approved; publishing and CI preflight in progress |
| A10 | Former settings worker: cmd/bria test files only | Stop fake Codex recursively executing whole test suite; exact helper regression | done; bounded red/green + whole cmd race |
| A11 | Controller worker: internal/integration/telegram_callback_acceptance_test.go | Async callback HTTP fake race reproduced then repeat green | done; whole integration race count=10 |
| A12 | Main: Makefile and push CI workflows | Provision pinned dependencies before offline checks; native test prerequisites | local done; actual push CI pending |
| A13 | Main: approval docs/fixtures only | No internal work-source identities or business values in public Git payload; raw receipts retained locally | done; sanitized full gate passed |
| A14 | CI worker: internal/sessionruntime orphan cleanup, its starter.go call/import; new internal/orphanresume | Linux runtime <=1850 with unchanged cleanup behavior | local done; Linux execution awaits CI |
| A15 | Main: internal/nativeterminal, scripts architecture checker/tests | Linux terminal <=510; architecture inspects all supported GOOS/GOARCH | local done; full gate passed |

Workers must not edit scripts or shared docs. Public API aliases/adapters preserve
consumer compatibility; report any required cross-owner edits to main. Main
registers new packages with justified bounded budgets, never raises old limits.
New packages must have a real responsibility, not merely relocate random lines.
Stop each zone at passing scoped checks and evidence handoff. No external
queries/live writes for workers. Final full verification only after integration.
Existing LUNA_APPROVAL_TODO and stabilization backlog remain preserved.

CI continuation uses the same contract and approved publication sequence. Main
owns integration, terminal cleanup simplification, checker, docs and release;
the independent worker owns only orphan-resume extraction and its tests. No
cross-owner edits, no live process operations by the worker. Stop at scoped
behavior checks and unchanged Linux package limits; then main runs full gates.
One worker plus main covers both independent code areas without shared writes.

Initial push receipt: remote main = 5d0a8b6ffe0efb4a1fbcf4bf5ecd0e96c502c8f9.
Platform run 34198434479 succeeded (8 jobs). Stage1 run 34198434480 failed:
Linux sessionruntime=1947/1850 and nativeterminal=512/510. No deploy occurred.
Hypotheses: platform-selected GoFiles differ (reproduce with Linux go list),
Go-version counting drift (compare same files locally), stale remote source
(compare SHA). Exact remote SHA matched; platform-specific files are present.
Add all supported platform graph checks so host-only acceptance cannot recur.

Confirmed: the same local Go toolchain reproduces both exact Linux counts when
go list selects Linux files; no version/counting drift is needed to explain CI.
New checker regression first failed all five cases (four target-specific caps
and a Linux/arm64-only forbidden import), then passed after checking all shipped
targets. The enhanced checker reproduced the original Linux CI errors locally.
Terminal cleanup now parses each discovered PID once and skips already-owned
identities before reading metadata; pinned-descriptor and parentage rechecks
remain intact. Linux terminal size is 507/510. Fresh macOS isolated-tmux race
tests passed; Linux/arm64 test executable compiles. Linux runtime execution of
the changed cleanup still requires the next CI receipt.

CI-fix integration: orphan-resume cleanup is now a stdlib-only platform module
(112/150 Linux lines); sessionruntime is 1842/1850 on Linux and macOS. Existing
Start request validation requires a prior binding before the direct resume
cleanup call. Removed only obsolete private wrappers, not a public API. Main
reviewed the exact selection/termination diff and Linux test child ownership.
Focused race/vet checks and Linux test cross-compilation passed. Canonical
make check-full passed again with all-platform architecture enforcement and
physical executable-trio acceptance. Next: commit/push scoped CI correction,
read actual Linux process-test and full push CI receipts, then approved deploy.

## Evidence and open boundaries

- Initial review: 3 forbidden edges and 10 oversized packages; 3 test race groups.
- A1-A7 scoped changes integrated. Architecture checker now PASS (all original
  13 categories removed); original controller race cases passed count=10.
- Full plain/race integration uncovered pre-existing fixture failures: recursive
  fake Codex launches in cmd/bria; unsynchronized async callback HTTP fake in
  internal/integration. Stopped only two exact test executable trees, never the
  working service. These directly block the requested full gate and are A10/A11.
- Current origin CI 34130051075 confirms missing module cache with GOPROXY=off.
  A12 prepares declared modules before offline checks, not loosening checks.
- Independent reviewers: screen/settings extraction approved; media/storage and
  controller reviews underway. Main inspects diffs and reruns consumer checks.
- Independent review: controller and screen/settings approved, scoped race suites
  plus 20 controller repetitions passed. Media/storage behavior and atomic writes
  verified. Exported internal TextDocumentPolicy intentionally moved from media
  core into documentproduction; its only production consumer (P4 composition)
  migrated atomically. A backwards alias would recreate the upward dependency;
  no compatibility with nonexistent out-of-module imports is claimed.
- Canonical Makefile now honors caller GOMODCACHE; prepare-deps verified declared
  modules and go.sum; checks remain offline. Build trio runs on macOS arm64;
  configuration check PASS; three other supported cross-build targets PASS.

## Measured package budgets (macOS production files)

| Package | Before | After | Unchanged cap |
| --- | ---: | ---: | ---: |
| telegramcontroller | 5758 | 5499 | 5500 |
| screenproduction | 444 | 135 | 200 |
| screen | 944 | 462 | 750 |
| telegrambridge | 1628 | 1167 | 1550 |
| storage | 1955 | 1890 | 1900 |
| telegram | 1805 | 1735 | 1750 |
| mediaproduction | 837 | 798 | 800 |
| settingscomposition | 237 | 166 | 200 |
| nativeadapter | 867 | 846 | 850 |
| settings | 813 | 710 | 800 |

New responsibilities have explicit bounded policies and reversed-edge rejection
tests. No AGENTS.md clause was removed or weakened. The package caps are guard
rails, not proof that all architecture debt in the repository is eliminated.

## Final-gate diagnostic note

First post-fixture full gate had one 5s readiness timeout in unchanged
TestNestedRuntimeFactoryAdapterCleanupKillsRawGrandchild. Ranked hypotheses:
resource contention (predicts isolated pass), helper/environment recursion
(predicts wrong helper dispatch), protocol regression (predicts repeated exact
failure). Helper has explicit runtime flags and immediate fixture dispatch;
the exact original test passed 10/10 in 4.999s total with no code/timeout edits.
Resource contention remains an inference, not an established cause. Repeat the
unminimized full gate; do not hide this evidence or raise production timeouts.

The repeated canonical command passed end-to-end, including the original nested
test, full race suite, vet, policy, architecture, packaging contracts, executable
trio and CLI acceptance:

```sh
env TMPDIR=/private/tmp GOMODCACHE=/Users/a-s-nosko/go/pkg/mod \
  VERSION=20260908-architecture-races make check-full
```

Read-only release preflight also passed check-config, getMe and validation of an
isolated copy of session state. Remote repository visibility is PUBLIC, so
approval corpus/docs are sanitized for publication. Original work-source
identities and values remain only in ignored `.cache/private-approval-evidence`.
Synthetic fixtures preserve menu shapes and wrapping; no raw work data will be
staged. Final source manifest: ARCHITECTURE_RELEASE_PLAN.md. Push/deployment
confirmation requested separately. Artem's subsequent "Делай" approves both
exact scopes in ARCHITECTURE_RELEASE_PLAN.md; no other targets are authorized.

Final handoff: canonical make check-full PASS again after sanitization, terminal
output `Bria executable trio acceptance: OK`. Production binaries are unchanged
by fixture-only sanitization. All local obligations are complete; only A9 and
its external A12 CI receipt remain open. Next: inspect/stage only manifest files,
commit/push without force,
monitor/reread CI; deploy the verified trio and restart only the recorded v2
LaunchAgent after its separate approval. Revalidate current refs/service/state
before writes because these preflight observations can drift.
- TDD: retain original reproductions, run focused checks before/after each fix.
- Release targets are established by ARCHITECTURE_RELEASE_PLAN.md. Publication,
  current-link switch and launchctl restart are serial operations owned by main;
  prior independent reviews are complete, so there is no useful parallel write.
- A7: pre/post adapter race tests green with isolated tmux outside sandbox.
  Added independent UTF-8 tail-budget and exact SHA256 fixtures before extraction;
  moved them to public nativecapture boundary after extraction. No old cap raised.
- Read-only release discovery: origin https://github.com/Time4Mind/bria.git,
  main and remote refs/heads/main both 86510f12205d4ccebc4d000833dbe924e4ddc140.
  LaunchAgent gui/501/com.time4mind.bria.v2 running, PID 38207 at preflight.
  Executable /Users/a-s-nosko/.local/opt/bria-v2/current/bria; current symlink
  targets releases/20260903095611-auth-visible. Config ~/.bria-v2/config.json,
  state ~/.bria-v2/state.json. Settings revision and session created_at present;
  actual current-code compatibility passed using an isolated state copy. No secrets copied.
