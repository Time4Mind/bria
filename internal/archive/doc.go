// Package archive defines Bria's native archive boundary.
//
// An archive is a Bria-owned artifact plus a manifest. It is not a
// provider session identifier and restoring it must not depend on provider
// retention.
//
// Archive follows a commit-before-stop protocol:
//
//  1. Runtime.ExportArchive produces a non-destructive, stable artifact.
//  2. Writer.Commit verifies size and digest and atomically publishes the
//     artifact and manifest. A visible manifest always has a complete artifact.
//  3. Runtime.DeactivateArchived stops the live runtime only after Commit.
//
// Commit and DeactivateArchived must be idempotent for the same archive ID.
// A failed Commit leaves the runtime live and no archive visible. A failed
// DeactivateArchived returns FinalizeError: the archive is durable, but the
// caller must reconcile the still-live runtime before changing domain state.
// The service deliberately performs no CCBot or provider-metadata import.
package archive
