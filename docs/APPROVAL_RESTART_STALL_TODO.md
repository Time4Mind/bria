# Approval restart stall

Task contract: 2026-09-09, Europe/Moscow. Exact result: the current Bria must
continue automatically accepting each distinct recognized Codex command approval
when `auto_approve_commands` is enabled, including after service restart and an
unverified preceding decision, while never replaying the same uncertain approval.

Sources: Artem's current report, the live `workdir6` terminal, current Bria state,
safe local logs and this repository. Unit: one exact logical session/provider
generation and each recognized approval fingerprint. Population: complete and
recognized incomplete command approvals, consecutive/parallel requests, stale
capture races and restart reattachment. Exclusions: generic pickers, persistent
approval choices, manual Enter, state/config edits and unrelated lifecycle work.

Allowed writes: this repository plus the standing-authorized normal release to
`Time4Mind/bria` and restart of `gui/501/com.time4mind.bria.v2` after all gates.
Acceptance: regression RED/GREEN, focused race suites, full `make check-full`,
exact manifest/commit/push, green required CI for the exact origin/main SHA,
matching installed artifacts, healthy sole service/lock holder, preserved state,
and a read-only live check that the previously pending approval no longer stalls.

Coverage map: integration owner owns runtime evidence, synthesis, tests/code,
release and postflight. Maxwell owns read-only parser/receipt analysis. Wegener
owns read-only settings/restart analysis. No agent writes. Stop when every row
has physical evidence; live approval content and secrets never enter logs/docs.

| ID | Required outcome | Status | Evidence / open boundary |
| --- | --- | --- | --- |
| A34.1 | Effective setting and exact active binding are verified | verified | Missing legacy field decodes ON; active `bdfac089...` is Codex generation 4 and its terminal survived the Bria restart |
| A34.2 | The current stall has an evidence-backed cause | verified | Live probe recognizes stable incomplete approval `8e8b87...`; flow turns every non-stale failure into generation-wide `approvalUncertain`; observer drops the error, so exact subtype is not logged |
| A34.3 | Same uncertain request is never replayed, but a distinct request is not blocked | verified | RED reproduced both blocked-next and replay-old failures; GREEN keeps a generation-scoped set of command-derived DecisionIDs, distinguishes collapsed commands, and preserves reflow/duplicate suppression; the live restarted session accepted five distinct approvals in generation 5 |
| A34.4 | Approval attempt/outcome is logged without command text | verified | Flow emits confirmed/stale/uncertain; five live `native.approval=confirmed` records contain only HMAC refs, generation and bounded lifecycle fields; safelog fail-closed tests pass |
| A34.5 | Full verification and standing-authorized release | verified | Runtime `d4d5430344564b5ffab919376014fcecc4969dc2`; versioned full gate and both exact-SHA CI passed; release `20260909-approval-restart-stall` runs as PID/sole lock holder 41157 with matching trio hashes |

Repeated error note: historical `session.recovery_failed` records repeat at a
high frequency in the retained critical log. They are outside the current approval
scope and the excerpt does not prove they belong to the current service window;
they remain a separate defect requiring bounded current-window investigation.

## Release receipt - 2026-09-09 19:14 UTC / 22:14 MSK

Runtime commit `d4d5430344564b5ffab919376014fcecc4969dc2` was pushed and
reread from `origin/main`. Stage 1 run `34392908545` and platform matrix run
`34392908729` both passed for that exact SHA. A second versioned
`TMPDIR=/private/tmp VERSION=20260909-approval-restart-stall make check-full`
passed, including the complete race suite and executable-trio acceptance.

The bounded install created only
`/Users/a-s-nosko/.local/opt/bria-v2/releases/20260909-approval-restart-stall`
with the three verified executables, switched `current`, and restarted only
`gui/501/com.time4mind.bria.v2`. Installed SHA-256 values match the checked
artifacts: Bria `47c7629c90e1...`, Codex adapter `f9e24dbe4363...`, Claude
adapter `54203c53f8b...`. The service remains running as PID 41157 and is the
sole state-lock holder; both live Codex adapters execute from the new release.
`check-config` and the read-only Telegram identity probe pass.

The active logical session `bdfac089...` kept provider session `01a08681...`.
Without manual terminal input, the approval visible before restart was accepted,
the command ran, generation advanced from 4 to 5, and four following distinct
approvals were also accepted in generation 5. Structured logs contain five
`native.approval=confirmed` events with HMAC references and no command/reason
payload. No detailed failure or critical event appeared after the new flow became
ready.

All seven session IDs and seven card IDs remain present. Settings, config and
LaunchAgent plist hashes are unchanged; journal growth is expected from the live
session. Historical `session.recovery_failed` repetition ends at 10:52 UTC and did
not recur in this release window. This does not prove perpetual absence of that
separate historical recovery defect.
