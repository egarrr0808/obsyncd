const { ItemView, MarkdownView, Notice, Plugin, PluginSettingTab, Setting } = require("obsidian");
const fs = require("fs");
const os = require("os");
const path = require("path");

const VIEW_TYPE = "obsyncd-conflict-review";
const MARK_START = "%%OBSYNCD_CONFLICT_START%%";
const MARK_LOCAL_START = "%%OBSYNCD_LOCAL_START%%";
const MARK_LOCAL_END = "%%OBSYNCD_LOCAL_END%%";
const MARK_REMOTE_START = "%%OBSYNCD_REMOTE_START%%";
const MARK_REMOTE_END = "%%OBSYNCD_REMOTE_END%%";
const MARK_END = "%%OBSYNCD_CONFLICT_END%%";

const DEFAULT_SETTINGS = {
  configPath: path.join(os.homedir(), ".config", "obsyncd", "config.yaml"),
  proposalDir: "",
};

module.exports = class ObsyncdConflictsPlugin extends Plugin {
  async onload() {
    this.settings = Object.assign({}, DEFAULT_SETTINGS, await this.loadData());

    this.registerView(VIEW_TYPE, (leaf) => new ConflictReviewView(leaf, this));

    this.addRibbonIcon("git-compare", "Open obsyncd conflict review", () => {
      this.openCurrentConflictReview();
    });

    this.addCommand({
      id: "open-conflict-review",
      name: "Open conflict review for current note",
      checkCallback: (checking) => {
        const file = this.activeMarkdownFile();
        if (!file) return false;
        if (!checking) this.openCurrentConflictReview();
        return true;
      },
    });

    this.addSettingTab(new ObsyncdConflictSettingsTab(this.app, this));
  }

  onunload() {
    this.app.workspace.detachLeavesOfType(VIEW_TYPE);
  }

  activeMarkdownFile() {
    const view = this.app.workspace.getActiveViewOfType(MarkdownView);
    return view && view.file ? view.file : null;
  }

  async openCurrentConflictReview() {
    const file = this.activeMarkdownFile();
    if (!file) {
      new Notice("obsyncd: open a Markdown note first.");
      return;
    }

    const localContent = await this.app.vault.cachedRead(file);
    const versions = await this.loadVersions(file.path, localContent);
    if (versions.length === 0) {
      new Notice("obsyncd: no conflict versions found for this note.");
    }

    const leaf = this.app.workspace.getLeaf("split", "vertical");
    await leaf.setViewState({
      type: VIEW_TYPE,
      active: true,
      state: {
        relPath: file.path,
        localContent,
        versions,
      },
    });
    this.app.workspace.revealLeaf(leaf);
  }

  async loadVersions(relPath, localContent) {
    const versions = [];
    versions.push(...this.loadProposalConflicts(relPath));
    versions.push(...parseMarkerVersions(localContent));
    return dedupeVersions(versions);
  }

  loadProposalConflicts(relPath) {
    const proposalDir = this.proposalDir();
    if (!proposalDir || !fs.existsSync(proposalDir)) return [];

    const out = [];
    const seenHub = new Set();
    for (const entry of fs.readdirSync(proposalDir)) {
      if (!entry.startsWith("conflict-") || !entry.endsWith(".json")) continue;

      const fullPath = path.join(proposalDir, entry);
      let conflict;
      try {
        conflict = JSON.parse(fs.readFileSync(fullPath, "utf8"));
      } catch (_) {
        continue;
      }
      if (!conflict || conflict.type !== "conflict") continue;
      if (normalizeRel(conflict.path) !== normalizeRel(relPath)) continue;

      const hubKey = `${conflict.server_hash || ""}\u0000${conflict.server_content || ""}\u0000${!!conflict.server_delete}`;
      if (!seenHub.has(hubKey)) {
        seenHub.add(hubKey);
        out.push({
          id: `hub:${entry}`,
          label: "Hub / server",
          subtitle: conflict.server_delete ? "deleted on hub" : "current canonical server version",
          content: conflict.server_delete ? "[deleted on hub]\n" : String(conflict.server_content || ""),
        });
      }

      out.push({
        id: `client:${conflict.target_device || "unknown"}:${entry}`,
        label: `Client ${shortDevice(conflict.target_device || "unknown")}`,
        subtitle: conflict.client_delete ? "deleted on client" : "proposed laptop version",
        content: conflict.client_delete ? "[deleted on client]\n" : String(conflict.client_content || ""),
      });
    }
    return out;
  }

  proposalDir() {
    if (this.settings.proposalDir && this.settings.proposalDir.trim() !== "") {
      return expandHome(this.settings.proposalDir.trim());
    }
    return path.join(path.dirname(expandHome(this.settings.configPath)), "state", "proposals");
  }
};

class ConflictReviewView extends ItemView {
  constructor(leaf, plugin) {
    super(leaf);
    this.plugin = plugin;
    this.state = { relPath: "", localContent: "", versions: [] };
  }

  getViewType() {
    return VIEW_TYPE;
  }

  getDisplayText() {
    return "obsyncd conflict review";
  }

  getIcon() {
    return "git-compare";
  }

  async setState(state, result) {
    this.state = Object.assign({ relPath: "", localContent: "", versions: [] }, state || {});
    await super.setState(state, result);
    this.render();
  }

  getState() {
    return this.state;
  }

  async onOpen() {
    this.render();
  }

  render() {
    const root = this.contentEl;
    root.empty();
    root.addClass("obsyncd-review");

    const header = root.createDiv({ cls: "obsyncd-review__header" });
    header.createDiv({ cls: "obsyncd-review__title", text: this.state.relPath || "obsyncd conflict review" });
    const count = this.state.versions.length;
    header.createDiv({
      cls: "obsyncd-review__message",
      text: count === 0
        ? "No alternate versions found. This note looks clean from the plugin side."
        : `${count} alternate version${count === 1 ? "" : "s"} found. This view is read-only for now.`,
    });

    if (count === 0) {
      root.createDiv({ cls: "obsyncd-review__empty", text: "No conflict data found for this note." });
      return;
    }

    const grid = root.createDiv({ cls: "obsyncd-review__grid" });
    this.renderPane(grid, "This laptop", "active local note", this.state.localContent, []);

    for (const version of this.state.versions) {
      const classes = diffLineClasses(this.state.localContent, version.content);
      this.renderPane(grid, version.label, version.subtitle, version.content, classes);
    }
  }

  renderPane(parent, title, subtitle, content, lineClasses) {
    const pane = parent.createDiv({ cls: "obsyncd-review__pane" });
    const paneHeader = pane.createDiv({ cls: "obsyncd-review__pane-header" });
    paneHeader.createSpan({ text: title });
    paneHeader.createSpan({ cls: "obsyncd-review__pane-subtitle", text: subtitle || "" });

    const pre = pane.createEl("pre", { cls: "obsyncd-review__code" });
    const lines = splitLines(content);
    if (lines.length === 0) lines.push("");
    for (let i = 0; i < lines.length; i++) {
      const cls = ["obsyncd-review__line"];
      if (lineClasses[i]) cls.push(`obsyncd-review__line--${lineClasses[i]}`);
      pre.createSpan({ cls: cls.join(" "), text: lines[i] || " " });
    }
  }
}

class ObsyncdConflictSettingsTab extends PluginSettingTab {
  constructor(app, plugin) {
    super(app, plugin);
    this.plugin = plugin;
  }

  display() {
    const { containerEl } = this;
    containerEl.empty();
    containerEl.createEl("h2", { text: "obsyncd Conflicts" });

    new Setting(containerEl)
      .setName("obsyncd config path")
      .setDesc("Used to infer the proposal directory when the field below is empty.")
      .addText((text) => text
        .setPlaceholder("~/.config/obsyncd/config.yaml")
        .setValue(this.plugin.settings.configPath)
        .onChange(async (value) => {
          this.plugin.settings.configPath = value;
          await this.plugin.saveData(this.plugin.settings);
        }));

    new Setting(containerEl)
      .setName("Proposal directory")
      .setDesc("Optional override. Usually ~/.config/obsyncd/state/proposals.")
      .addText((text) => text
        .setPlaceholder("~/.config/obsyncd/state/proposals")
        .setValue(this.plugin.settings.proposalDir)
        .onChange(async (value) => {
          this.plugin.settings.proposalDir = value;
          await this.plugin.saveData(this.plugin.settings);
        }));
  }
}

function parseMarkerVersions(content) {
  const versions = [];
  let pos = 0;
  let index = 1;
  while (pos < content.length) {
    const start = content.indexOf(MARK_START, pos);
    if (start < 0) break;
    const localStart = content.indexOf(MARK_LOCAL_START, start);
    const localEnd = content.indexOf(MARK_LOCAL_END, start);
    const remoteStart = content.indexOf(MARK_REMOTE_START, start);
    const remoteEnd = content.indexOf(MARK_REMOTE_END, start);
    const end = content.indexOf(MARK_END, start);
    if (localStart < 0 || localEnd < 0 || remoteStart < 0 || remoteEnd < 0 || end < 0) break;

    versions.push({
      id: `marker-local-${index}`,
      label: `Marker local ${index}`,
      subtitle: "embedded conflict block",
      content: content.slice(afterLine(content, localStart + MARK_LOCAL_START.length), localEnd),
    });
    versions.push({
      id: `marker-remote-${index}`,
      label: `Marker remote ${index}`,
      subtitle: "embedded conflict block",
      content: content.slice(afterLine(content, remoteStart + MARK_REMOTE_START.length), remoteEnd),
    });
    pos = end + MARK_END.length;
    index++;
  }
  return versions;
}

function diffLineClasses(localContent, versionContent) {
  const local = splitLines(localContent);
  const other = splitLines(versionContent);
  const classes = new Array(other.length).fill("");
  const pairs = lcsPairs(local, other);
  const matchedOther = new Set(pairs.map((pair) => pair[1]));

  for (let i = 0; i < other.length; i++) {
    if (matchedOther.has(i)) continue;
    if (i >= local.length) {
      classes[i] = "added";
    } else if (!local.includes(other[i])) {
      classes[i] = "changed";
    } else {
      classes[i] = "added";
    }
  }
  return classes;
}

function lcsPairs(a, b) {
  const rows = a.length + 1;
  const cols = b.length + 1;
  const dp = Array.from({ length: rows }, () => new Array(cols).fill(0));
  for (let i = a.length - 1; i >= 0; i--) {
    for (let j = b.length - 1; j >= 0; j--) {
      dp[i][j] = a[i] === b[j] ? dp[i + 1][j + 1] + 1 : Math.max(dp[i + 1][j], dp[i][j + 1]);
    }
  }
  const pairs = [];
  let i = 0;
  let j = 0;
  while (i < a.length && j < b.length) {
    if (a[i] === b[j]) {
      pairs.push([i, j]);
      i++;
      j++;
    } else if (dp[i + 1][j] >= dp[i][j + 1]) {
      i++;
    } else {
      j++;
    }
  }
  return pairs;
}

function splitLines(content) {
  if (!content) return [];
  const lines = content.replace(/\r\n/g, "\n").split("\n");
  return lines.map((line, index) => index === lines.length - 1 ? line : `${line}\n`);
}

function dedupeVersions(versions) {
  const seen = new Set();
  const out = [];
  for (const version of versions) {
    const key = `${version.label}\u0000${version.subtitle}\u0000${version.content}`;
    if (seen.has(key)) continue;
    seen.add(key);
    out.push(version);
  }
  return out;
}

function normalizeRel(value) {
  return String(value || "").replace(/\\/g, "/").replace(/^\.\//, "");
}

function expandHome(value) {
  if (!value) return value;
  if (value === "~") return os.homedir();
  if (value.startsWith("~/")) return path.join(os.homedir(), value.slice(2));
  return value;
}

function shortDevice(id) {
  return id.length <= 7 ? id : id.slice(0, 7);
}

function afterLine(text, pos) {
  if (text[pos] === "\r") pos++;
  if (text[pos] === "\n") pos++;
  return pos;
}
