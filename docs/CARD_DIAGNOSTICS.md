# Session-card diagnostics, schema 3

The original schema 2 covered callback and card transport diagnostics. Schema 3
adds typed archive and selection events for A23. Callback authorization, one-shot
claims and retry policy are unchanged; the archive behavior fix is documented in
the A23 section of ARCHITECTURE_RACE_TODO.md.

The active Bria config determines the directory: `<state_path>.logs`.
`service.jsonl` contains `telegram.flow_ready` (version 3) and loss notices;
`detailed.jsonl` contains `telegram.flow_stage`. A ready event is written
synchronously before the observer is returned; it proves the new logger can
write, not that a later interactive scenario succeeds.

## Read an incident

1. Find the latest `telegram.flow_ready`, record its `run_ref` and UTC time.
2. Filter detailed events by that run and the incident interval. `entity_id`
   links one operation across stages. `sequence` is logger delivery order;
   `time` is capture time, not delayed disk-write time. Concurrent operations
   may interleave; do not infer semantic order from sequence alone.
3. Find `callback.accept` failures and `callback.discard` (`result=skipped`).
   Ack completion alone does not prove a callback was executed.
4. Match `callback_ref` against `button_refs` on card events, and compare
   `card_ref` with `expected_card_ref`. The references are per-process HMACs;
   no raw signed callback, chat ID, session ID or message content is recorded.
   The same callback reference inside `button_refs` identifies the delivered
   keyboard containing the clicked button. Lists are bounded at 32 entries;
   `button_refs_truncated` reports omitted entries. No match in a truncated or
   expired log interval is not proof that the button was never registered.
5. Compare `card.send_started`/`card.edit_started`, `transport.send`/`transport.edit`,
   `card.finalize_started` and `state.commit`. The edit receipt precedes final
   card-state commit and keyboard binding. A successful `state.commit` is the
   binding-complete boundary for this prepared operation, not a promise that
   no newer operation overwrote it. `presentation_ref` identifies presentation
   ownership; `session_ref`, when available, identifies the session.
6. For navigation, inspect signed `action` and numeric `target`, then the
   rendered `page`, `pages` and `follow_latest`. Later commits with older page
   coordinates can reveal out-of-order refreshes; no generation is invented.

## Archive and selection chain

| Stage | What it proves |
| --- | --- |
| `session.archive_outcome` | Close returned scheduled, archived, deleted or failed; scheduled is not archival completion |
| `selection.fallback` | Candidate choice, empty selection, preservation of a newer selection/other node, or a failed search |
| `selection.persist` | Persistence call succeeded or failed; it is not a Telegram delivery receipt |
| `selection.project` | Card/list/native projection was built, or projection failed; compare its target to transport/card commit |

Join the initiating callback and these events by `entity_id` within one `run_ref`.
Deferred work retains the initiating operation context. Where a child operation
exists, `parent_operation_ref` joins it to the parent. No parent is inferred from
timing or a matching session alone. Session identity fields `session_ref`,
`previous_session_ref` and `target_session_ref` share the existing card HMAC domain;
`node_ref` uses a separate domain. All are process-local opaque references.

`selection_outcome` and `selection_reason` are closed enum vocabularies; unknown
values become `unknown`. No provider error text, prompt, title, workdir, raw IDs
or callback token is included. `candidate_count` is present only after a complete
same-node selectable-candidate scan, excluding the closing session. Missing is
not zero. A missing target denotes clear/list only when the stage/outcome says so.

For an incident, establish the close outcome, compare previous and target session,
check selection persistence, then projection and the existing transport/commit
chain. `projected` alone cannot establish that the recipient saw the card.
Persisted session `state_changed_at` is a domain timestamp that may be captured
before provider shutdown; it cannot timestamp the later disk write.

## Native failure and recovery (A25)

Runtime termination preserves the adapter's bounded, allowlisted stderr class.
`native_transcript_record_too_large` identifies a record/projection budget failure;
`native_transcript_read_limit` instead denotes an options/scan budget limit.
Malformed and binding failures have separate `native_transcript_malformed` and
`native_transcript_binding_invalid` classes. Raw stderr and transcript payloads
are never copied into these diagnostics. The reader's local typed limit error
contains offset/observed/limit counts, but runtime logs currently retain the safe
class only; do not infer missing sizes from that record.

`session.provider_failure` records the controller's observed runtime error with
the safe native failure reason and the existing session HMAC. It is emitted
before lifecycle handling. An accepted transport failure must not be logged or
handled as a known successful provider terminal. This typed event joins the
provider failure to recovery/card diagnostics without exposing the raw error.

`session.recovery_outcome` reuses schema 3's typed flow channel. Its
`selection_reason` distinguishes `startup_recovery`, `live_recovery` and
`manual_recovery`; `selection_outcome` records recovered, awaiting/unknown,
exhausted, preserved or failed results. It shares the card `session_ref` HMAC
domain. A manual callback carries its operation context; a live process exit
without an initiating callback must not be assigned an invented parent operation.

Follow recovery state observation through `selection.project` and the existing
transport/commit chain. An outcome log is not proof of a displayed card. Likewise,
provider-turn diagnostic records and Telegram flow records are distinct channels;
use the typed `session.provider_failure` session reference to bridge the channels,
and do not assume the separate service/flow operation refs are equal.
Repeat-error reporting is mandatory under [HARNESS.md](HARNESS.md): distinguish
separate occurrences, report count/period and impact, and retain an open todo.

## Callback rejection reasons

| Reason | Meaning |
| --- | --- |
| `token_invalid` | Invalid token encoding, size or signature |
| `token_expired` | Signed expiry passed |
| `token_fields_invalid` | Invalid decoded fields |
| `token_version_unsupported` / `token_action_unknown` | Unsupported token version or action |
| `presentation_missing` | Registry could not accept the token as a current/retired presentation |
| `presentation_replayed` | One-shot claim already used by a different callback, or replay outside exact recovery |
| `card_missing` | The expected session card was not found |
| `card_carrier_mismatch` | Stored card and incoming message/chat do not match |
| `callback_binding_mismatch` | Session, presentation or interaction binding does not match |
| `callback_origin_invalid` | Invalid callback origin/identity |
| `registry_failed` / `card_load_failed` | Registry/card-store operation failed |
| `card_commit_failed` / `presentation_bind_failed` | Post-send state or keyboard binding failed |
| `cancelled` / `deadline_exceeded` | Context cancellation/timeout without a more specific boundary reason |
| `operation_failed` | Other error; no free-form error is disclosed |

`presentation_missing` does not alone distinguish eviction, an unbound new
keyboard and a replaced carrier. Use the correlated send/bind events. Context
fields are absent if decoding failed before that context became trustworthy.
`retired` is emitted only when rejection metadata includes a matched
presentation; absence is not `false`. The original error remains `[REDACTED]`.

The observer remains bounded and non-blocking. `telegram.flow_trace_dropped`
reports queue overflow; `failed_count` reports prior failed writes on the next
successful record. A failed ready write prevents observer initialization.
No retained per-process key exists for joining identities across restarts.

## Acceptance boundary

Tests exercise real callback decoding, card binding, a mismatched carrier and
physical JSONL output; button correlation and secret/payload absence are checked
after writing. Additional tests cover typed error compatibility, unknown-code
rejection, bounded field formats, concurrent observer flushing and time order.
After deploying, check the new live ready record, process/lock and getMe, then
repeat the owner's pagination/menu scenario. Do not silently press buttons or
change callback rules merely to generate evidence.

A23 additionally tests actual controller + app lifecycle + durable store + signed
callback + flow delivery, then reopens state and reads physical JSONL. Fallback
target refs match the delivered card. Busy sessions switch immediately after the
accepted close transition but finish their request before provider shutdown.
Sequential and concurrent repeated closes cannot interrupt that work; failed
repeats cannot erase the initiating operation's correlation. This correlation is
process-local; a restarted process does not invent an original callback ID.
The local 20260908-archive-switch build is validated; its installation and the
owner's live Telegram reproduction have not been performed by A23.
