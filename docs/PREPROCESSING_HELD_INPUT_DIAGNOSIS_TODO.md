# A43 - preprocessing input held behind an old unknown

## Task contract

- Result: fix the current held lane and future equivalent incidents. After a
  proven total main-session failure and successful recovery/reconnect, every
  unresolved request present at that recovery boundary is permanently skipped
  regardless of its prior phase and is never replayed. Inputs accepted after
  recovery continue once in sequence.
- Sources: current deployed Bria safe logs, lifecycle/state metadata, durable
  message journal, process inventory and current repository code. User prompt
  and model-answer bodies are excluded.
- Period: the causal incident from 2026-09-10 18:23 MSK through the current
  observation on 2026-09-11, Europe/Moscow.
- Grain/population: one ordered durable-input lane for the active primary
  session, from Telegram ingress through preprocessing, main Codex admission
  and card projection. Other sessions are excluded except the shared satellite
  process needed to distinguish availability from queue blocking.
- Writes: repository code, tests and docs for this fix. No manual live state
  edits, prompt replay or terminal input. After full verification, the standing
  Bria release sequence authorizes commit, push, exact-SHA CI and deploy/restart
  of only `gui/501/com.time4mind.bria.v2`.
- Acceptance probe: after release recovery, reread the active session journal
  and prove that updates 721, 724 and 727 are terminal `skipped`, no old lease
  or provider replay occurred, the session is Ready, and a newly accepted
  post-recovery input remains eligible for one ordered dispatch.

## Coverage map

| Zone | Owner | Stop condition |
| --- | --- | --- |
| Journal/recovery implementation | integration owner | RED/GREEN public recovery test, durable skip and ordered post-recovery dispatch |
| Recovery seam audit | Dalton, read-only | exact post-reconciliation commit point and races reported |
| Persistence consumer audit | McClintock, read-only | validators, backup/restore and rollback set reported |
| Integration/release | integration owner | full gate, exact-SHA CI, deploy and live metadata postflight |

Production changes stay with one integration owner because they share the live
journal/recovery boundary. Two read-only audits run in parallel on disjoint
architecture questions. No external sources or expensive broad scans are needed.

## Todo and evidence

| ID | Obligation | Status | Evidence / open boundary |
| --- | --- | --- | --- |
| A43.1 | Reconstruct the current held-input timeline | verified | Update `783531727` completed Telegram ingress and durable card delivery at 07:34:14-15 MSK, but has no preprocessing or provider events and remains journal `pending` sequence 36. |
| A43.2 | Prove the ordering blocker | verified | Earlier update `783531721`, sequence 28, is `unknown`; `LeaseNextInput` returns `ErrNoAvailable` on the first `InputUnknown`, so later pending sequences 31 and 36 cannot lease. Empty leases rule out a currently executing processor. |
| A43.3 | Separate preprocessing availability from admission | verified | Update 721 was processed by Luna at 18:25:37-39 MSK (`ready` -> `responded` -> `accepted`); then the main Codex submit ended `provider_error`/`adapter_failed` at 18:25:59 and Bria entered a restart/recovery storm. Current shared Luna binding and its process exist; update 727 never reaches it because leasing stops first. |
| A43.4 | Implement recovery-boundary skip | verified | Durable cutoff opens before replacement can publish Ready; exact acceptance is fenced, all unresolved phases through the cutoff atomically become `skipped`, and later sequences remain pending. The current legacy blocked queue intentionally skips 721, 724 and 727 together. |
| A43.5 | Consumer compatibility and rollback | verified | Reopen validation rejects spoofed/incomplete recovery documents; backup reads marker+inputs+outputs atomically and fails closed while open; handoff bundle rejects an open boundary; skipped inputs are terminal and excluded from replay/export. Older binaries are rejected before rollback pointer changes because they cannot decode format v3. |
| A43.6 | Full verification and release | in progress | `make check-full` passed outside the sandbox, including all functional, architecture, packaging, vet and race checks; executable trio version `20260911-recovery-input-skip` was built. Remaining: push/exact-SHA CI, install/restart and live state/process/log proof under standing authorization. |

## Repeated-error finding

The 2026-09-10 incident was not a single harmless line. From 18:26:25 through
19:26:35 MSK the critical log contains 302 distinct `controller.stopped` events
and 604 `session.recovery_failed` events - approximately one restart cycle every
10 seconds with two failed recovery attempts per cycle. This requires a product
fix, not notification suppression. The current 2026-09-11 observation has no
fresh copy of that storm; its durable consequence is the retained old `unknown`
lane head. The count is bounded to that one-hour window in the retained local
critical log.

## Current conclusion

Preprocessing is enabled, but the latest request cannot invoke it. The durable
ordering fence stops before preprocessing on update 721. A42 made the later
root admissible in `RootReady`, but the earlier lease selector still returns
`ErrNoAvailable`, so that admission code is unreachable. The strict no-replay
rule remains necessary; the defect is the disagreement between recovery/root
eligibility and leasing. Card refresh cannot repair that state and only
re-renders the same pending history.
