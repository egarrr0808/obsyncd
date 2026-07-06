# obsyncd Project Rules

This document outlines workspace-specific rules and instructions for coding assistants developing the `obsyncd` Go local-first synchronization engine.

## Code Quality & Architecture Rules
- **Markdown Processing**: Preserve comments, tags, and formatting. Do not let markdown files be truncated or emptied during sync/conflict resolution unless explicitly requested by the user.
- **Import Cycle Avoidance**: `internal/guard` and `internal/proposal` have cross-dependencies. To test code inside `internal/proposal` that relies on `internal/guard`, use a separate package `proposal_test` for test files (e.g., `integration_test.go`).
- **Test Harness Visibility**: Export key methods used by the integration test harness (e.g., `Scan`, `DetectRemoteOverwrite`, `SnapshotPath`) by capitalizing their names. Keep internal implementations unexported (e.g., lowercase wrappers) to protect the main daemon interface.
- **Verification Priority**: Before committing changes to conflict staging, ingest, or state storage, compile and run `go test ./...` to prevent regressions.

## Synchronization Staging & Guard Rules
- Always protect local user data. If a conflict occurs, pause the folder to block Syncthing from silently overwriting the file.
- Stage the conflict in `.obsidian/obsyncd-staging/<hash>.remote` and prompt the user via `obsyncctl` for resolution.
- Never resume synchronization until all active conflicts for the path are resolved.
