# A56: voice input session binding

## Task contract

- Result: a voice message received while session A is selected remains owned by
  A through recognition and preprocessing, even if the user selects session B
  before recognition completes.
- Requirements: GitHub issue #2 and the current Bria implementation.
- Period/timezone: current repository and release on 2026-09-12, Europe/Moscow.
- Grain/population: one Telegram voice update and its complete path from ingress
  custody through durable input enqueue; Codex and Claude use the same path.
- Exclusions: no legacy project, no manual Telegram messages, no settings/state
  mutation, and no text/photo behavior change beyond preserving the ingress
  session through their existing preparation path.
- Allowed writes: current repository plus the standing Bria release sequence.
- Acceptance probe: a deterministic test blocks voice recognition in session A,
  selects B, releases recognition, and proves that durable input is stored for A;
  affected race tests, full gate, exact-SHA CI, signed local release and service
  postflight must pass.

## Coverage map

| Zone | Owner | Boundary | Stop criterion |
|---|---|---|---|
| Ingress ownership and enqueue | integration owner | `internal/telegramcontroller` | delayed result uses the captured session ID |
| Regression and race | integration owner | public controller flow | exact A -> B scenario is RED before and GREEN after the fix |
| Independent review | read-only reviewer | final diff | no open blocker/high/medium finding |
| Release | integration owner | GitHub and `com.time4mind.bria.v2` | exact SHA CI, install and postflight GREEN |

## Status

| ID | State | Evidence |
|---|---|---|
| A56.1 | done locally | deterministic test reproduced enqueue into B before the fix |
| A56.2 | done locally | voice and ordinary prepared-input paths call `enqueueSession` with ingress session A; voice, attachment and close-during-recognition race tests pass 20/20 |
| A56.3 | review and full gate done; release pending | independent review: approve; exact final-tree `VERSION=20260912-voice-session-binding make check-full`: GREEN; commit/push, exact-SHA CI and local release remain |

## Verified cause

The handler captured session A and passed it through recognition, prompt status,
and telemetry. The last step called the generic `enqueue`, which read the mutable
active session again. Selecting B during preparation therefore changed only the
final enqueue target. The fix preserves the already captured session ID at that
last boundary for both the asynchronous voice path and the ordinary preparation
path. If A closes meanwhile, existing `enqueueSession` availability checks reject
the input instead of redirecting it to B.
