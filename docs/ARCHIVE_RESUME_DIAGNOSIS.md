# Archive resume diagnosis 2026-09-09

## Follow-up: retained accepted observer blocks queued successor

Task contract: keep the old accepted receipt without replay, but when a newer
pending input already exists, recover the attached session as Ready and dispatch
that successor instead of starting a blocking observer for the historical turn.
Unknown/failed custody, close intent and accepted turns without a successor keep
their existing recovery barrier. Acceptance: focused RED/GREEN tests, full
`make check-full`, exact origin/main CI, install/restart of only
`gui/501/com.time4mind.bria.v2`, then journal/native/log proof that input
`telegram-update:783531603` advances while `telegram-update:783531558` is not
replayed.

- F6 verified: release `20260909-archive-root-unblock` fixed fresh-root admission,
  but startup attachment registered the older accepted continuation first,
  persisted generation 3 as Running and caused the newer pending input to defer.
- R6 code complete: explicit continuation eligibility is isolated in
  `internal/acceptedcontinuation`; the durable journal decides only the
  accepted-plus-newer-pending case, while session attachment owns the resulting
  Ready lifecycle state. Accepted without a successor and unknown custody retain
  observation; closing states never yield. RED/GREEN, focused race and full
  `make check-full` PASS at 13:53 UTC, including architecture and executable trio.
  Candidate `20260909-archive-successor-unblock` is darwin/arm64; SHA-256:
  Bria `f78b48bd451d22ef18e8c7b776d6a7d15da729bd335452caed2e7bdc2506c699`,
  Codex adapter `f68d871a964fe2fe2701317543ff9543ff16d45d080e0bbc519f677f851052c5`,
  Claude adapter `7c8eb38b4d6489ff12c24fb624da2adc265ead8ded18dada2ab574fff7b6388c`.
  No state or terminal input was edited manually. The final full gate after
  fail-closed protection for all retained unknown/failed custody also PASS.

## Follow-up release receipt

- Source `0e78f266418948ff78d7c88931ddb581f5982ba2` was pushed to
  `origin/main`; Stage 1 `34360617597` and Platform matrix `34360618129`
  completed successfully for that exact SHA.
- At 14:04 UTC release `20260909-archive-successor-unblock` replaced only the
  Bria executable trio and restarted `gui/501/com.time4mind.bria.v2`. PID 16335
  is stable and the sole state-lock holder; installed hashes match the checked
  candidate. Config/state checks and Telegram identity pass.
- Physical acceptance PASS: startup attached the same native session as
  generation 4, `telegram-update:783531603` advanced pending -> accepted ->
  completed and produced native turn `01a0867c-6239-7c11-8f31-5d1ab2c19994`
  plus a normal final. Session returned Ready at 14:08:21 UTC. The exact tmux
  panel showed the new `Продолжи.` execution; old `telegram-update:783531558`
  remained accepted/unknown and was not submitted again.
- Postflight retained 6 session identities, 6 cards, 337 total history records,
  19 inputs, config/plist/settings hashes and the live terminal. There were no
  critical/error events after the new `telegram.flow_ready`. R6 is complete.

### Release receipt CI follow-up

The docs-only receipt commit `699bf2e` kept Platform matrix green but exposed a
pre-existing Stage 1 race in `TestKillCurrentTreeKillsInheritedDescendantAndRejectsNonLeader`:
the helper published a PID with non-atomic `WriteFile`, so the reader could see
the file between truncate and write and decode an empty value. All helper PID
receipts now use write-then-rename atomic publication. The exact failing test
passes 100/100 under the race detector and the full `make check-full` passes.
This follow-up changes tests and Harness only; the installed runtime and hashes
remain the verified `0e78f26` release, so no repeat restart is required. Final
remote CI for the follow-up commit must be read live.

## Reopened user acceptance defect: new input blocked

2026-09-09 owner reports archive reopen succeeded but next request is not accepted.
Task contract: diagnose exact workdir5/session d61b7c05-923e-43e1-a369-a8571e619075
using current local code, logs and state from12:27UTC (15:27 Moscow). Read-only
production, no replay/restart/state edit. Acceptance is trace from owner ingress
through admission and native terminal; local diagnostic receipt only.
- F1 verified: archive resume completed12:27:40UTC; saved session ready with native
  01a084e6-518f-7e21-a292-958bb019269d generation2 since12:27:38UTC.
- F2 verified: new input783531603 sequence284 persisted pending; preprocessing
  completed12:27:51UTC. Prior783531558 sequence142 remains accepted, earlier551
  completed. No need for owner to send the same request again.
- F3 verified: acceptedinput.RootReady on read-only actual journal snapshot returns
  false,nil for input603 (public seam probe .tmp/archive-root-probe.go).
  It demands all earlier inputs completed/terminal_failed. Controller
  ProcessDurableInput calls this guard; bridge maps deferred to pending lease
  release. Archive guard fix did not address fresh-input admission after reopen.
- F4 verified: exact managed tmux binding matches session/native ID; pane%0 PID40192
  alive(node), displays interrupted previous conversation and idle input prompt.
  Bria PID44368 running/sole lock holder. No keys sent. Native header shows Sol,
  while preprocessing receipt reports Luna; model mismatch not investigated here.
- F5 complete: RootReady now ignores earlier InputAccepted records, which are
  already fenced from replay; InputUnknown/InputFailed/InputPending still block.
  Regression coverage updated through acceptedinput, durableflow and
  durablecomposition seams, including archive reopen successor admission.
  Full local gate passes; release follows the standing Bria authorization.
Separate log issue: one duplicate resume callback12:27:41 failed presentation_missing;
successful resume precedes it. This does not explain root_ready=false for new input.

## Final release receipt

R1-R4 complete at 2026-09-09 12:09 UTC (15:09 Moscow). Runtime source
42f0684bb79e6212dd558a389e790984e436b6bc is in origin/main; Stage1 CI34348964485
and Platform CI34348964526 passed. Full local gate and ungated fast-response
1000-repeat race test passed. Installed version20260909-archive-resume-quiet;
launchd gui/501/com.time4mind.bria.v2 running PID44368, same sole lock holder.
Installed SHA256: bria f16e969402d8f8c6034045241873e476b414ed63a21cdda07be34895aca1be7d;
Codex adapter f68d871a964fe2fe2701317543ff9543ff16d45d080e0bbc519f677f851052c5;
Claude adapter7c8eb38b4d6489ff12c24fb624da2adc265ead8ded18dada2ab574fff7b6388c.
All match local tested trio. Config/state compatibility and Telegram getMe pass;
fresh telegram.flow_ready receipt12:09:26UTC. Before/after snapshot hashes for
identities/cards/journal/config/plist/settings unchanged:6 sessions,6 cards,
300 history records,18 inputs. Only Bria service restarted; no Codex terminal kill
or manual lifecycle/journal edit. R5 remains an explicit unverified user boundary:
owner archive-click and real provider resume have not been executed after release.
This receipt is docs-only and needs no second binary deployment.

## Historical diagnosis and release iterations

Scope: diagnose failed archive resume of Bria session
`d61b7c05-923e-43e1-a369-a8571e619075`; local source and live receipts,
read-only production checks, no replay or lifecycle mutation.

- D1 complete: reproduce native receipt interpretation through public reader.
  `telegram-update:783531558` returns unknown, no final, no proven failure;
  transcript Scan succeeds (155 events). Earlier input 783531551 is completed.
- D2 complete: locate gate. acceptedrecovery/archive_guard.go calls CheckResume
  before Base.Resume. resume_guard.go rejects nonterminal accepted outcomes.
  Journal retains last input as accepted. Native transcript contains task_started
  for 01a084fb-77f7-78c3-84c5-72b06b853f88, but no task_complete or turn_aborted.
- D3 complete: correlate user failures. Detailed log at 11:05:28 and 11:14:22 UTC
  shows resume_session failing in controller callback after 201/205 ms;
  raw cause is redacted. Generic callback effect fence produces effect_unknown.
- D4 implementation complete: owner authorized implementation and removal of observation-loss
  card text. Separate explicit
  archive reopen from prior-input replay eligibility; retain unknown prior outcome
  and prevent automatic input replay. Add a
  regression test for archived session with accepted input lacking terminal proof.

Separate diagnostic backlog: generic callback errors are redacted to
operation_failed; typed classification would improve future diagnosis.

Implementation contract: explicit archive reopen may proceed with pending accepted
outcome while retaining acceptance and never replaying that input. Persist available
finals first, retain binding/CAS and storage-error checks. Suppress nonterminal CLI
disconnect prose, retain genuine terminal/authentication failures and diagnostics.
Owner: parent owns acceptedrecovery and release; Galileo owns turnfinalization text
and telegramcontroller failure-notification seam. Independent write sets.
Acceptance: pending archive -> ready, retained accepted input not leaseable, late
final persisted, binding changes rejected, quiet observation loss, full gate/CI,
current service restart with state retention. Live UI remains unverified until tested.
Release targets: origin/main Time4Mind/bria and gui/501/com.time4mind.bria.v2 only,
trio under existing /Users/a-s-nosko/.local/opt/bria-v2/releases. No secret/config edits.
Integration complete. RED/GREEN and focused race pass for archive guard,
accepted main+steer restart/late-final flow, observation loss, and queue continuation.
Legacy untyped observation notice is hidden by card projection; user/model text
and stored history are retained. Independent archive review approved.
Full make check-full passed 2026-09-09, including race, architecture, packaging
and executable trio, version 20260909-archive-resume-quiet.
Source 99e27792c2f7ce22cb7f2ec1ee836cbc3feea6d2 pushed to origin/main.
Platform CI 34346577290 passed. Stage1 34346577291 failed in existing
nativeadapter fixture: TestAttachOnlyReadyDoesNotReadOversizedExistingTranscript
received adapter completion before consuming Close frame. Deterministic handshake
reproduced this harness race; scanner completion and frame draining now precede
EOF failure. Exit and scanner errors are preserved. RED/GREEN, repeated focused
race and full nativeadapter race passed; independent review approved. Only tests
changed in CI follow-up. Galileo owned test harness, Mencius reviewed read-only,
parent owns integration. No runtime or release target expansion.
CI fix is pushed as fd3a47b2f0e139b09d65b8ffdd7dbfdfc0ae0d86. Fresh full
make check-full passed, including race and executable trio; runtime hashes unchanged.
- R1 complete: implementation, regression tests, independent review, full local gate.
- R2 complete: code and CI fixture fix committed/pushed; remote main reread exact SHA.
- R3 pending external: as of 2026-09-09 11:51 UTC GitHub has created no Actions
  runs or check suites for fd3a47b, despite enabled push workflows. Previous SHA
  CI failure is not evidence against the fixed test. Do not call latest CI green.
- R4 pending: install trio and restart gui/501/com.time4mind.bria.v2 after CI;
  verify version/hashes, running process/sole lock holder and state retention.
  Current service remains PID68226, release 20260909-persistent-terminal-final2.
  Candidate 20260909-archive-resume-quiet passes check-config/check-state.
- R5 unverified boundary: real owner archive-click/provider resume after release.
  No live session has been reopened, no old accepted input has been replayed.
Preflight at 11:42 UTC: 6 sessions, 6 cards, 300 history entries, 18 journal inputs;
config/settings and identities unchanged. Prepared local release helper is
`.tmp/archive-release.rb`; reread current service/paths/hashes before using it.

11:56 UTC follow-up: CI started for docs SHA0fde645. Platform matrix34347792046
passed; Stage134347792011 passed normal tests but race detected unsynchronized
projectionUIState test maps (SetCardPrompt versus AppendCardHistory), in
TestTechnicalDisplayToggleFiltersOnlyTypedToolsAndKeepsHistory. Native harness
fix passed. Galileo owns telegramcontroller test-only correction, Mencius reviews,
parent integrates and reruns gates. Release remains pending.
Additional acceptance failure: after mutex-only correction, ungated instant-provider
test failed functionally 1/1000 under race. Worker received input before prompt
publication; a subsequent history load could replace fast response projection.
Parent made the minimal fallback enqueue ordering fix: publish processed prompt
before queue send, retaining rejected-prompt updates for full/closed queue. No
delay added to provider fixture. Contract clarification announced to owner/agents;
scope remains card history correctness, no new feature or external target.
Ungated original test now passes all1000 race repetitions; complete controller
race passed14.362s. Independent review approved runtime reorder and synchronized
fixture consumers. Fresh full make check-full PASS after all corrections, including
race, architecture, packaging and executable trio. Release waits for latest CI.

Native app-server probe in previous turn did not obtain a resume response and
does not establish provider health. Live restoration has not been verified.
Prior claim that Luna probe PID 48913/48927 belonged to this exact session was
unsupported: its tmux name identified a separate bria-luna-probe session, and
matching workdir alone did not establish provider identity.
