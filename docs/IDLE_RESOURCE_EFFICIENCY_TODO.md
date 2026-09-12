# A58: full idle resource efficiency without behavior changes

## Task contract

- Result: reduce Bria parent and native-adapter idle wakeups, CPU and disk I/O
  without changing user-visible behavior or the existing worst-case reaction
  bounds for prompt, approval, output, terminal exit, recovery or user input.
- Sources: current `main`, macOS `proc_pid_rusage` measurements, safe structured
  logs, deterministic behavioral tests and the existing A50 performance audit.
- Period/timezone: current local service on 2026-09-13, Europe/Moscow.
- Grain/population: Bria parent plus all Bria native adapters and their short-lived
  helper processes; stable idle, active turn, interactive approval, terminal exit
  and restart/recovery. Provider model execution is measured separately.
- Exclusions: no Telegram test messages, config/state/secrets mutation, provider
  model optimization, weaker durability, reduced capture content or relaxed
  error detection.
- Allowed writes: current repository; after complete verification, the standing
  Bria commit/push/CI/signed local deploy sequence.
- Acceptance: public-seam RED/GREEN proves unchanged reaction behavior and fewer
  redundant operations; focused race and full gates pass; exact-SHA CI, signed
  install and postflight pass; the same live idle probe shows a material wakeup
  reduction with no new critical errors.

## Coverage map

| ID | Zone | Owner and boundary | Stop criterion | State |
|---|---|---|---|---|
| A58.1 | Native adapter/tmux cost | read-only investigator; `nativeadapter`, terminal implementation | exact redundant calls and no-latency replacement identified | complete |
| A58.2 | Parent recurring work | read-only investigator; parent timers/sweeps only | remaining parent wakeups mapped and safe candidates ranked | complete |
| A58.3 | Behavioral/performance proof | read-only investigator; tests/benchmarks only | public seams cover active, approval, output, exit and idle cost | complete |
| A58.4 | Integration | integration owner; implementation with disjoint file ownership | RED/GREEN and focused race pass without behavior changes | complete |
| A58.5 | Release and live acceptance | integration owner | full gate, review, exact-SHA CI, signed deploy and repeat probe GREEN | in progress: candidate verified |

## Baseline

- Window `2026-09-13 00:10:45-00:11:15 MSK`, 30 seconds, no safe-log events.
- Bria parent plus six adapters: `0.94%` of one core including reaped helper CPU,
  `98.0 MiB` physical footprint, zero disk read/write during the window.
- Own process wakeups: `2428.4/s`; five polling adapters contribute roughly
  `442-470/s` each. The fixed adapter ticker is `150 ms`.
- Since service start: `5.29 MiB` physical reads, `60.18 MiB` physical writes;
  average `5.23/59.58 KiB/s`. The idle windows themselves performed no disk I/O.

## Invariants

1. Do not increase the current 150 ms scheduling bound on any state that can
   produce user-visible terminal changes.
2. User input must wake processing immediately; no polling interval may delay it.
3. Terminal exit and approval/output detection retain their current public
   timing contracts and recovery semantics.
4. Durable state, journal and safe-log ordering/fsync guarantees remain unchanged.

## Confirmed design

- Stable idle is driven by one persistent non-mutating `tmux -u -N -C ...
  attach-session -f ignore-size` client per native adapter. It emits only
  coalesced opaque change hints; terminal payload is neither retained nor
  exposed by the observer, and its API cannot issue arbitrary tmux commands.
- Active turns keep the existing 150 ms transcript/liveness polling path.
  User input remains request-channel driven, and screen capture keeps the
  existing 300 ms throttle with a one-shot deadline so a coalesced hint cannot
  be lost.
- Any observer startup failure or unexpected disconnect while the exact
  terminal is still alive immediately restores the old 150 ms polling path.
  Exact terminal death keeps the existing failure/recovery result.
- The parent currently contributes about `155 wakeups/s`, `0.07%` of one core
  and zero idle disk I/O. Its periodic safety work is retained because removing
  it would weaken durable recovery for negligible measured gain; the full fix
  targets the five adapters responsible for roughly `93%` of wakeups.

## Verification evidence

- RED: `TestStartUsesNonMutatingControlModeAndPublishesInitialHint` timed out
  against the initial observer stub on 2026-09-13.
- Existing real native terminal and adapter suites pass outside the filesystem
  sandbox after integration; the sandbox itself cannot create their IPC sockets.
- Isolated real-tmux probe confirmed control mode preserves `120x40` geometry
  and reports output/pane exit. An ambient `TMUX` duplicate was found
  in the test environment and is being removed by the fixture, not hidden in
  product behavior.
- Public-seam tests pass three consecutive runs for healthy idle observation,
  asynchronous screen output, startup failure fallback, post-attach disconnect
  fallback, bounded active ordering and a deliberately hanging observer that
  does not delay the first request.
- Independent review found and blocked release on two behavior risks. A strict
  order test reproduced screen output preceding active transcript receipts;
  active hints now defer to the established `poll -> screen` path and the test
  is GREEN for ten consecutive runs. A late-stall test reproduced an extra
  fallback interval; exact Alive is now followed by immediate throttled capture
  before polling resumes, GREEN for five runs.
- A 75 ms payload-free non-mutating heartbeat detects two missing control replies
  within the existing 150 ms liveness bound. It launches no helper process and
  a post-attach stall deterministically returns to the old polling path.
- Exact macOS CI on tmux 3.7c reproduced that `attach-session -r` makes external
  Bria `send-keys` fail with `client is read-only`. RED/GREEN compatibility
  probes on tmux 3.6a and 3.7c use `-f ignore-size`: geometry remains `120x40`,
  input remains available and the observer itself still issues only the fixed
  `display-message` heartbeat.
- Focused `-race` for `tmuxobserver`, `nativeterminal`, `nativeadapter` and
  architecture checks is GREEN after both review fixes. Independent re-review
  is APPROVE with no open blocker/high/medium findings. Final
  `VERSION=20260913-idle-event-observer make check-full` is GREEN on the exact
  release candidate, including global race, operational/supply-chain checks and
  executable trio.
