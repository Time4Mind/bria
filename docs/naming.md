# Naming decision: Bria

Status: accepted on 2026-08-10; `Bria` / `bria` is the sole current name.

The product and every operator-facing invocation use **Bria** / `bria`:

| Surface | Identifier |
| --- | --- |
| Repository/directory | `bria` |
| Go module | `github.com/Time4Mind/bria` |
| Python distribution | `bria` |
| CLI executable | `bria` |
| Environment prefix | `BRIA_` |
| Default data directory | `~/.bria` |

All operator-controlled paths, service accounts, and environment variables use
`bria`: `~/.bria`, `/etc/bria`, `/var/lib/bria`, the `bria` account, and the
`BRIA_` prefix. Deployments must migrate old operator paths before upgrading;
runtime code does not depend on or fall back to the former names.

Persisted wire identifiers are versioned compatibility data, not operator
names. They must never be used to derive filesystem paths, service users,
package names, commands, or environment variables.
