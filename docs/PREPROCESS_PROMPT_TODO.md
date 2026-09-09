# A30: replace repository default preprocessing instruction

## Contract

Source: Artem's exact Russian prompt in the current request, 2026-09-09
Europe/Moscow. Result: replace only the repository default speech-cleanup
instruction verbatim. Preserve configured custom instructions, preprocessing
enablement, provider selection, isolation and durable already-accepted payloads.
Unit: instruction in one newly encoded preprocessing request. No analytical
period/denominator; release timestamps use UTC/Moscow. No legacy sources.

Allowed writes: bounded default constant, public encode/decode regression test,
this todo/status pointer, then automatic normal push to Time4Mind/bria main,
exact-SHA CI, verified trio installation/restart of existing local
gui/501/com.time4mind.bria.v2. No config/state/secrets edits, deletions, live model
prompts or outgoing user messages. Acceptance: exact literal default reaches
payload decode, custom text is preserved, targeted tests/full gate/CI pass;
installed version/hashes/process/lock/logs/getMe and data preservation verified.

## Coverage and ownership

Main owns internal/promptpreprocess/preprocess.go and preprocess_test.go,
docs and all Git/deploy. One-constant change is not usefully split between
writers. Bernoulli independently reads default consumption/settings behavior
and reviews the bounded diff; no edits, provider calls or duplicate test runs.
For release, Carver independently reviews main's ignored one-shot install helper
(exact new artifact/PID/target identities and bounded asynchronous stop wait).
Main alone writes the helper and performs installation; no additional repo files.
Stop at exact default/custom preservation proof or a concrete blocker. No broad
history or unrelated feature work. Existing A29 obligations remain closed.

## Obligations

| ID | Acceptance | Status | Evidence |
|---|---|---|---|
| A30.1 | Exact requested default, custom instruction unchanged | released | Default/blank literal RED->GREEN; exact prompt in installed/running binary; custom/settings unchanged |
| A30.2 | Targeted/full tests and independent review | complete | Independent review approve; targeted race/full make check-full and physical binary PASS |
| A30.R | Push/CI/install/restart and preservation postflight | complete | 1cbe166 both CI success; new version running PID83890/sole lock; artifact/getMe/logs/preservation PASS |

## Evidence and release manifest

RED: exact Encode/Decode regression returned the old default for empty and blank
configuration; custom case passed. GREEN after one constant replacement.
Targeted race: promptpreprocess0.593s/controller0.361s; full helper race0.190s
and command race0.596s. Independent consumer review confirms already encoded
durable envelopes retain their exact instruction and no duplicated old default.
No LLM output-quality claim; no model call required for literal replacement.

Read-only live settings: preprocessing_enabled=true, preprocessing_instruction
empty. Therefore newly encoded requests after installation use the new default;
no runtime settings migration/write is needed. At06:01:56UTC previous service is
navigation-follow, PID23498 running, 5sessions ready3/archived2,5cards/history146,
16completedinputs and unchanged preservation hashes from A29 receipt.

Exact manifest: internal/promptpreprocess/preprocess.go,
internal/promptpreprocess/preprocess_test.go, docs/PREPROCESS_PROMPT_TODO.md,
docs/STATUS_AND_NEXT.md. Direct main in https://github.com/Time4Mind/bria.git;
normal commit/push, both exact-SHA CI, install trio20260909-speech-cleanup-prompt
to /Users/a-s-nosko/.local/opt/bria-v2/releases/20260909-speech-cleanup-prompt,
switch existing current and restart only gui/501/com.time4mind.bria.v2 using
existing plist. Retain previous release; preserve config/state/settings/journal.
After stop, wait boundedly for asynchronous launchctl removal before switching;
do not reuse A29's old PID, target or immediate-removal assumption.
Terminal: current main SHA CI green, installed artifact identity/process/lock,
safe logs/getMe and before/after preservation proof. No unrelated writes.

Full make check-full PASS 2026-09-09 06:04 UTC: policy/source-secret scan,
format/architecture, plain/vet/packaging, race and physical trio acceptance.
Race cmd65.423s, integration10.621s, controller7.697s, scripts14.636s; unchanged
packages may use Go cache. Version20260909-speech-cleanup-prompt; exact literal
present in compiled bria binary, check-config succeeds. SHA256:

- bria: 0298c9a885f0cfdfd7aefffa53b5cd0adf7f51cfc3112b7998d0c03341376458
- bria-codex-adapter: 792059a14d750aae8367d7b23d77434e0bc3cd604d01e7040e1f9800d4f41911
- bria-claude-adapter: 88d612486c8c3171489dd369673a2e93e05f6b84aa97092bc2403af850648263

## Installed release receipt

Source1cbe166a6294677e18684ead91e2413d657c46c7 pushed/read back in origin/main.
Both exact-SHA CI completed successfully:34317609232 Platform build matrix,
34317609160 Stage 1 checks. Direct main, no separate merge required. Independent
default-consumer and one-shot install-helper reviews approve within scope.

At06:10:44UTC 2026-09-09 guarded stop began; service absence/process exit/lock
release confirmed06:10:49UTC before pointer switch/bootstrap. At06:11:09UTC
(09:11:09 Europe/Moscow), speech-cleanup-prompt is running PID83890/runs1/never
exited, sole lock holder83890. All installed hashes match above; getMe OK.
Main lsof confirms this process maps the new release's actual executable; the
exact requested literal is present in the installed binary.

Before/stopped/after snapshots have identical six preservation hashes:
identities, cards, journal, settings, config and plist. Retained5sessions
(ready3/archived2),5cards/146history,16completedinputs. No manual data/config
changes. Previous navigation-follow release retained. Already encoded requests
keep their prior instruction; new requests use the new default with the current
empty instruction setting. No live speech-quality/model-output test was run.

Receipt is a docs-only follow-up; unchanged installed binaries do not require
another deployment. GitHub checks for the final documentation HEAD still apply.

Independent postflight06:11:59UTC confirms healthy service and preservation;
main reread flow_ready06:10:50.015 and two startup recovered receipts at
06:10:51.196/06:10:52.037. No errors/damaged records in the checked startup
window. Two preceding shutdown live_recovery/skipped are not failures. Journal
outputs344=confirmed257/superseded83/unknown4, unchanged, pending finals/leases0.
All A30 obligations complete; no custom setting or existing request was rewritten.
