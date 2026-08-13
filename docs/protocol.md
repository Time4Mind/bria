# Agent protocol

> The Python hub/agent protocol below is retained for prototype compatibility.
> The production Go cluster uses Raft mTLS plus bounded node-control and
> enrollment endpoints.

## Go node enrollment

The CA issuer exposes TLS 1.3 endpoints on its enrollment address:

- `POST /v1/enrollment/register` consumes one 30-minute invitation and submits
  a signed node contract as a pending request;
- `POST /v1/enrollment/status` requires a fresh Ed25519 proof and returns no
  certificate while pending;
- after Telegram approval, status returns a certificate bound to that public
  key, the public CA certificate, callback key, and enabled peer addresses.

Invitation plaintext never enters Raft: state stores only SHA-256, expiry and
consumption time. Pending nodes have no certificate, vote, session metadata,
Telegram token, provider credential, or transcript access. The CA private key
remains issuer-local. If the issuer is not leader, it forwards only the
validated request over the existing mutually authenticated node-control
channel; the leader rejects enrollment forwarding from every other member.
Contract enrollment uses the same proof and approved bundle, with a
manually returned short-lived claim instead of the original invitation.

Protocol version `1` uses typed JSON envelopes over an outbound agent-to-hub
WebSocket connection, so managed hosts need no inbound public port. Production
deployments use `wss://`; plain `ws://` is intended for localhost and networks
with a separate trusted TLS terminator.

Every envelope contains `version`, `kind`, `host_id`, `request_id`, `sequence`,
`sent_at`, and an object `payload`.

Connection flow:

1. Agent opens a WebSocket with subprotocol `bria.v1`, header
   `X-Bria-Host-ID`, and `Authorization: Bearer <host token>`.
2. Hub authenticates the `(host_id, token)` pair during the HTTP upgrade. Tokens
   are never included in protocol envelopes or error messages.
3. Agent sends `hello` with the same host ID, version, and capabilities. The hub
   rejects a mismatch and binds that connection permanently to the host ID.
4. Agent sends a complete session `snapshot` when reconciliation requests it.
5. Hub sends allowlisted `command` envelopes and receives correlated `result`
   or `error` envelopes. Many requests may be in flight; `request_id` is unique
   within the channel and correlates each response.
6. Agent emits sequenced `event` envelopes; hub acknowledges durable progress.
7. Reconnect resumes after `last_acked_sequence`, followed by reconciliation.

## Durable event delivery

Each host agent owns a persistent event spool. `append(Event)` writes the event
to disk before it becomes eligible for transmission and assigns a strictly
increasing `Envelope.sequence` starting at `1`. Event envelopes always use
`kind: "event"`, a positive sequence, and an empty `request_id`.

After `hello`, the agent replays every event above its last cumulative ACK in
sequence order, then sends newly published events on the same channel. The
`hello.last_acked_sequence` value describes the agent's persisted ACK boundary,
not merely an in-memory connection state.

The hub acknowledges an event only after its consumer has successfully handled
and durably recorded it:

```json
{
  "kind": "ack",
  "host_id": "server-a",
  "request_id": "",
  "sequence": 42,
  "payload": {"acked_sequence": 42}
}
```

ACKs are cumulative: sequence `42` confirms all event sequences through `42`.
The envelope and payload sequences must agree. Stale ACKs are idempotent;
zero, negative, or never-assigned sequences are protocol errors. Applying an
ACK atomically compacts confirmed events from the agent spool while preserving
the next sequence across restarts.

The current spool is a compact JSON state file replaced atomically after each
append or ACK and written with mode `0600`. A failed write doesn't advance the
in-memory sequence or discard pending events.

Delivery is at least once. If an ACK is lost, an event may be replayed after
reconnect, so the hub consumer must deduplicate by `(host_id, sequence)`. The
transport never auto-ACKs merely because it received an event;
`WebSocketHubChannel.acknowledge_event(sequence)` is an explicit consumer action.

## Hub event consumer

`HubEventConsumer` is an application service outside the WebSocket transport.
One instance is bound to one already-authenticated host ID. Its synchronous
`consume(envelope)` method returns an ACK envelope only after the updated state
has been saved successfully. The supervisor can then pass that ACK's cumulative
sequence to `WebSocketHubChannel.acknowledge_event()`.

The hub persists `Host.last_event_sequence`. It applies only the exact next
sequence, ACKs an already-applied sequence without saving again, and rejects a
gap. Wrong-host, non-event, malformed, unsafe-transition, and unknown-session
messages raise `ProtocolError` without advancing durable progress.

`session_announced` is the only event allowed to introduce an unknown session.
`session_changed` updates allowlisted session metadata. `session_output` and
`interactive_prompt` only advance `last_event_at`; transcript or prompt bodies
are never copied into `ProjectState`.

After a non-duplicate event is durably applied, the consumer publishes a
transient `AppliedHostEvent` to hub-side sinks such as Telegram. Sink delivery
is decoupled from transport acknowledgement: a slow or unavailable Telegram
API cannot withhold ACK or cause agent replay. Sinks re-render from
authoritative state/runtime data rather than treating the event body as
durable UI state.

The transport enforces finite opening, hello, request, ping, closing, incoming
message-size, and concurrent-command limits. Version and JSON validation happen
when each frame is decoded. An invalid handshake receives HTTP 401; malformed
post-upgrade traffic closes with WebSocket code 1002. A second live connection
for the same host is closed with application code 4009.

TLS is injected with standard `ssl.SSLContext` instances on both server and
client. The hub stores only SHA-256 digests of high-entropy host tokens and the
CLI supports issue/rotate/revoke. Agent reconnect and snapshot reconciliation
are wired, as are durable agent event spooling and cumulative ACK/replay. Live
credential reload and certificate provisioning remain reliability-layer work.

The implementation follows the current `websockets` asyncio APIs for
[`serve()`](https://websockets.readthedocs.io/en/17.0.1/reference/asyncio/server.html)
and
[`connect()`](https://websockets.readthedocs.io/en/17.0.1/reference/asyncio/client.html).
