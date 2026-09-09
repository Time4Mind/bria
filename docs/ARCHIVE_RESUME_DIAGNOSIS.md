# Archive resume diagnosis 2026-09-09

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
- D4 in progress: owner authorized implementation and removal of observation-loss
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
Remaining: resolve CI failure, verify latest CI, deploy/postflight.

Native app-server probe in previous turn did not obtain a resume response and
does not establish provider health. Live restoration has not been verified.
Prior claim that Luna probe PID 48913/48927 belonged to this exact session was
unsupported: its tmux name identified a separate bria-luna-probe session, and
matching workdir alone did not establish provider identity.
