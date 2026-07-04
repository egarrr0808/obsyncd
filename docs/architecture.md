# obsyncd Architecture Blueprint

Status: canonical design for implementation.

## Rules

- Engine: `github.com/syncthing/syncthing v1.30.0`.
- Allowed Syncthing imports:
  - `github.com/syncthing/syncthing/lib/syncthing`
  - `github.com/syncthing/syncthing/lib/protocol`
  - `github.com/syncthing/syncthing/lib/events`
  - `github.com/syncthing/syncthing/lib/config`
  - `github.com/syncthing/syncthing/lib/model`
  - `github.com/syncthing/syncthing/lib/fs`
- Forbidden imports:
  - `github.com/syncthing/syncthing/gui/...`
  - `github.com/syncthing/syncthing/cmd/syncthing/...`
- UI: none. Daemon is managed by config files plus local Unix socket on Linux/macOS and named pipe on Windows.
- Storage: no-CGO BoltDB-compatible store for last clean base snapshots.
- Merge policy: never silently overwrite divergent Markdown.

## Topology

Clients sync Obsidian vaults with one static Oracle Cloud VPS Syncthing node.

Client config:

```xml
<device id="ORACLE_DEVICE_ID" name="oracle-vps" introducer="true">
  <address>tcp://vps.example.com:22000</address>
</device>
```

Oracle VPS config:

- Static public DNS or IP.
- Syncthing listener on TCP/UDP `22000`.
- Firewall:

```bash
ufw allow 22000/tcp
ufw allow 22000/udp
```

The Oracle node shares the Obsidian folder with every client. Clients mark Oracle as `introducer="true"`. Oracle does not mark clients as introducers.

## Daemon Modules

```text
cmd/obsyncd/              process entrypoint
internal/core/            Syncthing app boot, config, lifecycle
internal/diffmerge/       3-way Markdown line merge
internal/interceptor/     conflict artifact event loop
internal/store/           base snapshot DB
internal/control/         local socket/named-pipe CLI API
```

## Interceptor Flow

1. Subscribe to Syncthing events via `events.Logger`.
2. Watch `ItemFinished`, `LocalChangeDetected`, and folder state events.
3. If path matches `*.sync-conflict-*.md` or `*.sync-conflict-*.markdown`, derive canonical note path.
4. Take per-file lock.
5. Pause the folder or remote Oracle device through internal model/config control. REST config patch is fallback only.
6. Read:
   - base text from BoltDB snapshot store
   - local text from canonical note
   - remote text from conflict artifact
7. Run 3-way line merge.
8. Write canonical note atomically:
   - temp file in same directory
   - write bytes
   - file `fsync`
   - close
   - rename over canonical note
   - best-effort parent directory `fsync`
9. Delete conflict artifact only after canonical write succeeds.
10. Request internal Syncthing rescan for canonical path.
11. Resume paused folder/device.
12. Release lock.

If base snapshot is missing, merge uses safe two-sided conflict markers around full local and remote content.

## Conflict Markers

```text
<<<<<<< LOCAL
local text
=======
remote text
>>>>>>> REMOTE
```

## Platform Rules

- Use `filepath.Clean`.
- Reject absolute artifact paths.
- Reject paths escaping folder root.
- Preserve existing newline style where possible.
- Detect Windows/macOS case collisions before deleting artifacts.
- Do not require CGO.

## Required Tests

- clean auto-merge where only local changed
- clean auto-merge where only remote changed
- identical local/remote
- overlapping edits produce conflict markers
- artifact path maps to canonical note path
- atomic write does not delete artifact when write fails
- artifact deleted after successful write
- rescan requested after merge
- Windows-style path rejection/cleaning behavior

