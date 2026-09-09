# Archive resume diagnosis 2026-09-09

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
