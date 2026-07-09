# obsyncd Conflicts Obsidian Plugin

Read-only conflict viewer for obsyncd.

## What it does

- Opens a side pane beside the active Markdown note.
- Shows the local note plus hub/client versions from obsyncd conflict JSON files.
- Supports any number of conflict versions for the same note.
- Highlights lines in alternate versions that differ from the local note.
- Also understands old embedded `%%OBSYNCD_CONFLICT_START%%` marker blocks.

This first slice does not resolve conflicts or write files.

## Install for local testing

Copy this folder into your vault:

```bash
mkdir -p /path/to/Vault/.obsidian/plugins
cp -r obsidian-plugin/obsyncd-conflicts /path/to/Vault/.obsidian/plugins/
```

Then in Obsidian:

1. Settings -> Community plugins.
2. Turn off Restricted mode if needed.
3. Enable `obsyncd Conflicts`.
4. Open a note and run `Open conflict review for current note`.

By default it reads:

```text
~/.config/obsyncd/state/proposals
```

You can override this in the plugin settings.
