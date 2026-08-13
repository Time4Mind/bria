# Security

## Trust model

Bria is currently a single-owner system. Every enrolled node is a trusted
cluster member, while Telegram updates from every user except the configured
owner are ignored. Claude, Codex, and other session backends run with the OS
permissions of the Bria service account on their own node and are therefore
inside this trust boundary. Use a dedicated, unprivileged account where the
platform permits it.

Node-to-node Raft and control traffic uses mutual TLS 1.3. Certificates are
bound to the cluster and exact node identity. Runtime commands, transcripts,
and session files sent between nodes are accepted only from the current
leader. A session file may be read only from that live session's resolved
working directory and is size-limited.

## Secrets and personal data

Do not commit runtime configuration, Telegram tokens, node or CA private keys,
certificates, databases, logs, transcripts, archives, provider credentials, or
real deployment addresses and node names. The ignore rules cover their common
local forms, but they are not a substitute for review.

CI scans the complete Git history with Gitleaks and checks reachable Go
dependencies with `govulncheck`. Examples and tests must use reserved domains,
reserved documentation addresses, and synthetic identities only.

If sensitive data is committed, removing it in a later commit is insufficient.
Rotate the affected credential, rewrite the repository history, and verify the
rewritten history before pushing.

## Reporting

Keep the repository private and report suspected vulnerabilities directly to
the repository owner. Do not include live credentials, personal data, or full
production logs in an issue.
