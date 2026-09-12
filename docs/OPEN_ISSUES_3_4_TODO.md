# A57: GitHub issues #4 and #3

## Task contract

- Result: fix #4 so a full 512-entry card accepts and replays live runtime
  events without metadata corruption or a recovery loop; fix #3 so completion
  of background session B cannot change active card A.
- Sources: GitHub issues #4 and #3, current repository `main`, deterministic
  regression tests, and safe post-deploy logs.
- Period/timezone: current code and local service on 2026-09-12,
  Europe/Moscow.
- Grain/population: one accepted unfinished turn with commentary/tool/question/
  thinking at full card capacity; one active card A plus one completing
  background session B, including delayed/concurrent delivery.
- Exclusions: GitHub issue #1, `bria-legacy`, manual Telegram messages,
  secrets, config/state mutation, and unrelated UI/lifecycle changes.
- Allowed writes: current repository and the standing-authorized Bria release
  sequence after both fixes are complete.
- Acceptance: each exact scenario is RED before and GREEN after its fix;
  metadata and carrier identities remain exact; affected race suites, full
  gate, independent review, exact-SHA CI, signed install and service postflight
  pass.

## Coverage map

| Zone | Owner | Boundary | Stop criterion |
|---|---|---|---|
| #4 bounded runtime history | integration owner | `telegramhistory` / `cardeventhistory` / storage | 512-entry insert and replay remain bounded and aligned |
| #4 lifecycle consequence | read-only investigator | observer and recovery path | root-cause chain and sufficient public regression seam identified |
| #3 background notification | read-only investigator | notification planning, retirement and Telegram flow | exact foreign-card mutation source and identity fence identified |
| Integration and release | integration owner | final diff, full repository and local service | no open review finding; release/postflight GREEN |

No expensive external probes or Telegram writes. Agents have disjoint read-only
zones; the current Codex session is the single integration owner.

## Status

| ID | State | Evidence |
|---|---|---|
| A57.1 | implemented | #4: bounded insertion reserves one runtime or all final parts, preserves active/queued anchors and pending finals, compacts old completed entries with aligned metadata, and retains exact replay identity |
| A57.2 | approved | #3: background completion is ineligible for retirement; shared carrier owned by another active session is fenced at capture and execution, including legacy replay; independent review APPROVE |
| A57.3 | verified | Exact RED covered 513 entries, two-part final capacity, mixed-index eviction and shared-carrier deactivation; focused GREEN repeated 20-100x, controller acceptance, affected `-race` and full `20260912-history-notification-safety make check-full` GREEN |
| A57.4 | releasing | Independent reviews APPROVE; exact manifest, commit/push, exact-SHA CI and signed local release remain |

## Deferred issue

GitHub #1 remains open and excluded because its own task explicitly requests
issue documentation only and says that a product fix was not requested.
