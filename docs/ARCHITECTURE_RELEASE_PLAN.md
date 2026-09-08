# Bounded release plan - 2026-09-08

Status: APPROVED by Artem's "Делай" on 2026-09-08 after the two separate
Git and deployment scopes were presented. Execution receipts are tracked in
ARCHITECTURE_RACE_TODO.md. Source is local main based on
86510f12205d4ccebc4d000833dbe924e4ddc140. Includes prior requested approval/config
work plus current architecture/race fixes. No unrelated changes, secrets,
runtime state, cache, binaries or credentials belong in Git.

Repository visibility verified PUBLIC. Approval docs/fixtures use synthetic
work-source identifiers and omit business values; raw evidence is local-only.

## Git sequence (separate confirmation)

Review and stage the task file manifest below plus this plan. Commit locally;
push only HEAD:refs/heads/main to https://github.com/Time4Mind/bria.git, without
force. Reread remote SHA. Monitor push CI; scoped CI-fix/test iterations may be
committed and pushed to the same branch. Terminal criterion: exact remote SHA
matches verified local source and push CI completes successfully. No tags,
GitHub release publication, remote deletions or branch rewriting.

## Local deployment (separate confirmation, after Git gate)

Install only the verified macOS arm64 executable trio into new directory
/Users/a-s-nosko/.local/opt/bria-v2/releases/20260908-architecture-races.
Keep the old release /Users/a-s-nosko/.local/opt/bria-v2/releases/20260903095611-auth-visible.
Verify SHA256, executable versions, config compatibility, and a fresh isolated
copy of persisted state before switching current. Atomically change only
/Users/a-s-nosko/.local/opt/bria-v2/current to the new release; restart only
gui/501/com.time4mind.bria.v2 via launchctl. Never invoke the generic packaging
service helper, whose hardcoded label differs from this installed service.
Do not edit /Users/a-s-nosko/.bria-v2/config.json, credentials or persisted
state. Normal startup may write its own operational state under that config.
Do not restart CCBot, other bots or user tmux sessions; no manual outbound message.
Terminal criterion: reread current SHA256, new live PID and process executable,
single-instance lock, stable process and no startup error; read-only getMe check.
If startup fails, stop and report; switching back/restarting again requires a
fresh decision under the project's destructive/deploy rules.

## Task source manifest

Final task/test evidence updates to these files are included; new unrelated
targets require a new confirmation. Runtime data and .cache are excluded.

Full-gate fixture/regression additions (within the CI-fix sequence):

- `internal/nativeapproval/public_fixtures_test.go`
- `scripts/architecture_refactor_test.go`

- `.github/workflows/context.yml`
- `.github/workflows/platform.yml`
- `Makefile`
- `README.md`
- `cmd/bria-approval-probe/live_settings_test.go`
- `cmd/bria-approval-probe/main.go`
- `cmd/bria-approval-probe/main_test.go`
- `cmd/bria/main_test.go`
- `docs/ARCHITECTURE_RACE_TODO.md`
- `docs/CONFIGURATION.md`
- `docs/DECISIONS.md`
- `docs/LUNA_APPROVAL_CASES.md`
- `docs/LUNA_APPROVAL_TODO.md`
- `docs/PRODUCT.md`
- `docs/STATUS_AND_NEXT.md`
- `docs/examples/bria-single-machine.json`
- `docs/luna-probe-prompt.txt`
- `internal/config/documented_example_test.go`
- `internal/documentproduction/document_test.go`
- `internal/documentproduction/policy.go`
- `internal/integration/telegram_callback_acceptance_test.go`
- `internal/mediaproduction/document_test.go`
- `internal/mediaproduction/runtime.go`
- `internal/mediaproduction/runtime_test.go`
- `internal/nativeadapter/adapter.go`
- `internal/nativeadapter/adapter_test.go`
- `internal/nativeadapter/approval_test.go`
- `internal/nativeadapter/screen_observation.go`
- `internal/nativeapproval/capture.go`
- `internal/nativeapproval/capture_test.go`
- `internal/nativeapproval/codex_approval.go`
- `internal/nativeapproval/codex_approval_test.go`
- `internal/nativeapproval/presentation.go`
- `internal/nativeapproval/testdata/codex-command-approval-collapsed.txt`
- `internal/nativeapproval/testdata/codex-command-approval-tracker-description.txt`
- `internal/nativeapproval/testdata/codex-command-approval-two-options.txt`
- `internal/nativeapproval/testdata/codex-command-approval-wiki-metadata.txt`
- `internal/nativeapproval/testdata/codex-command-approval-wiki.txt`
- `internal/nativeapproval/testdata/codex-command-approval-yt-rows.txt`
- `internal/nativeapproval/testdata/codex-command-approval.txt`
- `internal/nativeapprovalflow/flow.go`
- `internal/nativeapprovalflow/flow_test.go`
- `internal/nativeapprovalflow/robustness_test.go`
- `internal/nativecapture/capture.go`
- `internal/nativecapture/capture_test.go`
- `internal/nativecli/approval_policy_test.go`
- `internal/nativecli/codex_approval_test.go`
- `internal/nativecli/launch_policy_test.go`
- `internal/nativecli/plan.go`
- `internal/nativecli/plan_test.go`
- `internal/nativecli/presentation.go`
- `internal/nativecli/ready.go`
- `internal/nativecontrolport/port.go`
- `internal/nativerender/native.go`
- `internal/nativerender/native_renderer.go`
- `internal/nativerender/native_renderer_test.go`
- `internal/nativescreencache/cache.go`
- `internal/nativescreencache/cadence_test.go`
- `internal/nativescreencache/refresh_test.go`
- `internal/nativeterminal/expand_test.go`
- `internal/nativeterminal/terminal.go`
- `internal/p4runtimecomposition/composition.go`
- `internal/p4runtimecomposition/composition_test.go`
- `internal/providerpreferences/preferences.go`
- `internal/runtimeprotocol/native.go`
- `internal/runtimeprotocol/native_approval_test.go`
- `internal/screen/native.go`
- `internal/screen/native_renderer.go`
- `internal/screen/native_renderer_test.go`
- `internal/screen/native_test.go`
- `internal/screen/screen.go`
- `internal/screenproduction/cadence_test.go`
- `internal/screenproduction/composition.go`
- `internal/screenproduction/native.go`
- `internal/screenproduction/native_concurrency_test.go`
- `internal/sessionruntime/native.go`
- `internal/sessionruntime/native_approval_identity_test.go`
- `internal/sessionruntime/native_observation.go`
- `internal/settings/codec.go`
- `internal/settings/codec_compatibility_test.go`
- `internal/settings/settings.go`
- `internal/settings/settings_test.go`
- `internal/settingscodec/document.go`
- `internal/settingscomposition/native_approval_test.go`
- `internal/settingscomposition/preferences.go`
- `internal/settingscomposition/preferences_test.go`
- `internal/settingscomposition/provider_compatibility_test.go`
- `internal/settingsport/port.go`
- `internal/storage/session_store.go`
- `internal/storage/turn_history_test.go`
- `internal/telegram/rich.go`
- `internal/telegram/rich_test.go`
- `internal/telegrambridge/callback_compatibility_test.go`
- `internal/telegrambridge/presenter.go`
- `internal/telegramcallbackview/view.go`
- `internal/telegramcallbackview/view_test.go`
- `internal/telegramcontroller/controller.go`
- `internal/telegramcontroller/controller_test.go`
- `internal/telegramcontroller/native_approval.go`
- `internal/telegramcontroller/native_observer.go`
- `internal/telegramcontroller/native_terminal.go`
- `internal/telegramcontroller/preprocessing_test.go`
- `internal/telegramcontroller/technical_history_test.go`
- `internal/telegramhistory/history.go`
- `internal/telegramhistory/history_test.go`
- `internal/telegrampipeline/pipeline.go`
- `internal/telegramrich/rich.go`
- `internal/telegramruntimecomposition/approval_mapping_test.go`
- `internal/telegramruntimecomposition/composition.go`
- `internal/telegramsettingsview/view.go`
- `internal/telegramsettingsview/view_test.go`
- `internal/telegramturnhelpers/durable.go`
- `internal/telegramturnhelpers/durable_test.go`
- `internal/telegramturnhelpers/preparation.go`
- `internal/telegramui/keyboard.go`
- `scripts/build_environment_test.go`
- `scripts/check_repo.go`
- `scripts/check_repo_test.go`
