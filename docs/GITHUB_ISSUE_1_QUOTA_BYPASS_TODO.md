# GitHub Issue 1: Codex quota polling with bypass

Task contract: 2026-09-09, Europe/Moscow. The exact target is
[`Time4Mind/bria#1`](https://github.com/Time4Mind/bria/issues/1). When a local
Codex work-session command contains the explicit approval/sandbox bypass, the
work-session configuration must remain unchanged while the read-only quota
collector launches a compatible `app-server --stdio` command without that
session-only flag. The existing Nodes and Status presentation code is excluded.

Sources of requirements and evidence: issue 1, current repository code and
tests, current macOS service postflight, and GitHub CI. Population: Codex quota
commands with and without bypass, including already configured `app-server` and
`--stdio`. Exclusions: Claude quota collection, Telegram rendering, secrets,
unrelated native-session policy, and the unsupported full CCBot flag set. The
current upstream native-session policy still normalizes bypass to
`on-request/workspace-write`; this fix does not add a new launch mode. It keeps
the configured argv immutable so the same quota fix also applies to the cited
local Linux build where explicit bypass support already exists. Authorized
writes: this repository and the standing-authorized release to
`gui/501/com.time4mind.bria.v2`. Acceptance:
an observed RED/GREEN process-boundary regression, neighboring tests, full
`make check-full`, exact commit/push and green required CI, matching installed
binary hashes, healthy process/lock, and preserved user state.

Coverage map: the integration owner owns the todo, `internal/providerquota`,
integration, Git and release. Read-only reviewer A owns the argv lineage from
configuration through native work-session and quota launches. Read-only
reviewer B owns the regression matrix and compatibility risks. Their work does
not overlap and only the integration owner synthesizes conclusions.

| ID | Required outcome | Status | Evidence / open boundary |
| --- | --- | --- | --- |
| GH1.1 | Quota polling omits the Codex bypass flag | verified | Process-boundary regression failed with no RPC response before the fix and passed after filtering the flag from the quota-process copy |
| GH1.2 | Work-session command configuration is not mutated | verified | Regression asserts that the configured argv slice remains byte-for-byte unchanged; native launch policy is outside this fix |
| GH1.3 | Existing quota configurations remain compatible | verified | Empty argv and existing `app-server` / `--stdio` cases pass; neighboring composition and providerquota race suites pass; live Codex `account/rateLimits/read` succeeds outside sandbox |
| GH1.4 | Full verification and standing-authorized release | verified | Runtime commit `f67434d06c5978268df42bdfa4636f29cd89ebb8`; full gate, Stage 1 `34377468915` and platform matrix `34377468892` passed; release `20260909-codex-quota-bypass` runs as PID/sole lock holder `4746` with matching trio hashes |

Review decision: both read-only reviewers confirmed the quota-copy boundary and
that Nodes/Status are untouched. Broader filtering of the full CCBot argv was
rejected as scope expansion: current Bria intentionally rejects its hook-bypass
flag and has no literal CCBot launch mode. The exact issue reproduction contains
only `--dangerously-bypass-approvals-and-sandbox`; supported non-session options
remain unchanged rather than being silently discarded.

Release postflight preserved all 7 session IDs, 7 card IDs, 20 journal input
identities, config, settings, plist and the active tmux session. A fresh
read-only `account/rateLimits/read` against the local Codex CLI returned the
account quota after deployment. The deployed macOS configuration itself has no
bypass flag, so the exact Linux ARM64 bypass-enabled runtime remains verified by
the process-boundary regression and cross-build CI, not by mutation of that
external installation. The service log file remained unchanged since
2026-09-07; it contains historical repeated errors but no fresh release event.
