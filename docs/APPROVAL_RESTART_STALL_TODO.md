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
| A34.3 | Same uncertain request is never replayed, but a distinct request is not blocked | verified locally | RED reproduced both blocked-next and replay-old failures; GREEN keeps a generation-scoped set of command-derived DecisionIDs, distinguishes collapsed commands, and preserves reflow/duplicate suppression; review blockers resolved |
| A34.4 | Approval attempt/outcome is logged without command text | verified locally | Flow emits confirmed/stale/uncertain; Telegram trace stores HMAC session/provider/approval refs plus generation, with safelog fail-closed tests |
| A34.5 | Full verification and standing-authorized release | local gate passed | Fresh `TMPDIR=/private/tmp make check-full` PASS including race and trio build; exact-SHA CI, install/restart and live postflight remain |

Repeated error note: historical `session.recovery_failed` records repeat at a
high frequency in the retained critical log. They are outside the current approval
scope and the excerpt does not prove they belong to the current service window;
they remain a separate defect requiring bounded current-window investigation.
