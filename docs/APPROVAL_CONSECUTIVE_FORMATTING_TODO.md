# Consecutive approval and Rich rendering fix

Task contract: 2026-09-09, Europe/Moscow. Source of requirements and evidence:
Artem's current request, the live `workdir6` Codex terminal, current Bria state,
safe service logs and this repository. The result is one Bria release in which
distinct consecutive Codex command approvals in the same provider generation
are independently auto-approved when the setting is enabled, while disabled
mode and unknown picker types remain untouched. Approval cards must preserve
command structure and render shell syntax literally through the uniform Rich
Markdown transport.

Population: complete and recognized incomplete Codex command approvals,
including direct approval-to-approval transitions and stale-screen races.
Exclusions: generic model/settings/question pickers, persistent approval rules,
manual state edits, secrets and unrelated session lifecycle behavior. Writes are
limited to this repository, its normal release in `Time4Mind/bria`, and the
standing-authorized local service `gui/501/com.time4mind.bria.v2` after all gates
pass. Acceptance: behavioral RED/GREEN tests, focused race suites, full
`make check-full`, exact manifest commit/push, green required CI for origin/main,
installed artifact hash match and healthy service/lock/log postflight.

Coverage map: integration owner owns `internal/nativeapprovalflow`, adapter
error mapping and release; the formatting worker's result was integrated behind
the `internal/telegramnativeview` presentation boundary; a read-only reviewer
covered approval safety edge cases. No other writer may edit these areas during
the task.

| ID | Required outcome | Status | Evidence / open boundary |
| --- | --- | --- | --- |
| A33.1 | Distinct consecutive approvals in one generation are each accepted once | verified | Live YT -> YQL transition exposed missing receipt handoff; RED/GREEN now requires receipt plus a different active fingerprint to confirm the first Enter |
| A33.2 | Stale/unknown outcomes cannot duplicate an Enter | verified | Typed pre-Enter stale retries only on changed hash; unknown post-Enter outcome blocks the generation |
| A33.3 | Auto-approve OFF and foreign pickers remain untouched | verified | Focused OFF, duplicate and generic-picker regressions pass |
| A33.4 | Approval text is literal and structurally readable in Rich Telegram wire | verified | Dedicated view bounds every literal block and the whole Rich card; transport test requires `sendRichMessage` |
| A33.5 | Full verification and standing-authorized release | in progress | Fresh `make check-full` passes after the live receipt-handoff fix; exact follow-up CI and redeploy remain |
