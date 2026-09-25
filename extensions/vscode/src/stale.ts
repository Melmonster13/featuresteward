import * as vscode from "vscode";
import type { Flag } from "./api";
import { staleLabel } from "./hover";
import { findUses, looksLikeText, maxFileBytes, type Use } from "./scan";

// Folders that hold dependencies or build output, not your code.
const skip = "**/{node_modules,.git,dist,out,build,vendor,bin,target,.venv,venv,__pycache__}/**";
const maxFiles = 20_000;

type Node = { kind: "flag"; flag: Flag } | { kind: "use"; uri: vscode.Uri; use: Use } | { kind: "none" };

// StaleFlags shows stale flags and where the workspace uses them, and
// marks those uses in open editors with a hint.
export class StaleFlags implements vscode.TreeDataProvider<Node>, vscode.Disposable {
  private stale = new Map<string, Flag>();
  // Uses of stale keys, by file.
  private uses = new Map<string, { uri: vscode.Uri; uses: Use[] }>();
  private scans = 0;
  private readonly changed = new vscode.EventEmitter<void>();
  readonly onDidChangeTreeData = this.changed.event;
  private readonly hints = vscode.languages.createDiagnosticCollection("featuresteward");
  private readonly disposables: vscode.Disposable[];

  constructor(private readonly log: vscode.LogOutputChannel) {
    this.disposables = [
      this.changed,
      this.hints,
      vscode.workspace.onDidSaveTextDocument((doc) => void this.scanFile(doc.uri)),
      vscode.workspace.onDidOpenTextDocument((doc) => this.hint(doc)),
      vscode.workspace.onDidChangeTextDocument((e) => this.hint(e.document)),
      vscode.workspace.onDidCloseTextDocument((doc) => this.hints.delete(doc.uri)),
      vscode.workspace.onDidChangeWorkspaceFolders(() => void this.scan()),
    ];
  }

  dispose(): void {
    for (const d of this.disposables) d.dispose();
  }

  // update takes the latest flags, and rescans the workspace if the set of
  // stale flags changed.
  update(flags: Iterable<Flag>): void {
    const next = new Map([...flags].filter((f) => f.stale).map((f) => [f.key, f]));
    const same = next.size === this.stale.size && [...next.keys()].every((k) => this.stale.has(k));
    this.stale = next;
    if (same) {
      this.changed.fire(); // details such as the suggestion may have changed
      return;
    }
    void this.scan();
  }

  // scan finds uses of stale keys in every file in the workspace.
  async scan(): Promise<void> {
    const id = ++this.scans;
    const keys = new Set(this.stale.keys());
    const found = new Map<string, { uri: vscode.Uri; uses: Use[] }>();
    if (keys.size > 0 && vscode.workspace.workspaceFolders?.length) {
      const files = await vscode.workspace.findFiles("**/*", skip, maxFiles);
      if (files.length === maxFiles) this.log.warn(`Only the first ${maxFiles} files were searched for stale flags.`);
      for (const uri of files) {
        if (id !== this.scans) return; // a newer scan started
        const uses = await usesIn(uri, keys);
        if (uses.length > 0) found.set(uri.toString(), { uri, uses });
      }
    }
    if (id !== this.scans) return;
    this.uses = found;
    this.log.info(`${plural(keys.size, "stale flag")}, used in ${plural(found.size, "file")}.`);
    this.changed.fire();
    for (const doc of vscode.workspace.textDocuments) this.hint(doc);
  }

  private async scanFile(uri: vscode.Uri): Promise<void> {
    if (this.stale.size === 0 || !vscode.workspace.getWorkspaceFolder(uri)) return;
    const uses = await usesIn(uri, new Set(this.stale.keys()));
    if (uses.length > 0) this.uses.set(uri.toString(), { uri, uses });
    else if (!this.uses.delete(uri.toString())) return;
    this.changed.fire();
  }

  // hint marks stale keys in an open document, from its unsaved contents.
  private hint(doc: vscode.TextDocument): void {
    if (doc.uri.scheme !== "file" && doc.uri.scheme !== "untitled") return;
    const uses = findUses(doc.getText(), new Set(this.stale.keys()));
    this.hints.set(
      doc.uri,
      uses.map((u) => {
        const flag = this.stale.get(u.key)!;
        const d = new vscode.Diagnostic(
          new vscode.Range(u.line, u.start, u.line, u.end),
          `Stale flag ${u.key} (${staleLabel(flag.stale!.reason)}): ${flag.stale!.suggestion}`,
          vscode.DiagnosticSeverity.Hint,
        );
        d.source = "FeatureSteward";
        return d;
      }),
    );
  }

  getChildren(node?: Node): Node[] {
    if (!node) {
      return [...this.stale.values()].sort((a, b) => a.key.localeCompare(b.key)).map((flag) => ({ kind: "flag", flag }));
    }
    if (node.kind !== "flag") return [];
    const out: Node[] = [];
    for (const { uri, uses } of [...this.uses.values()].sort((a, b) => a.uri.path.localeCompare(b.uri.path))) {
      for (const use of uses) if (use.key === node.flag.key) out.push({ kind: "use", uri, use });
    }
    return out.length > 0 ? out : [{ kind: "none" }];
  }

  getTreeItem(node: Node): vscode.TreeItem {
    switch (node.kind) {
      case "flag": {
        const { flag } = node;
        const item = new vscode.TreeItem(flag.key, vscode.TreeItemCollapsibleState.Expanded);
        item.description = `${staleLabel(flag.stale!.reason)} · ${flag.steward ? `@${flag.steward}` : "no steward"}`;
        item.tooltip = `${flag.name}\n\nStale since ${flag.stale!.since.slice(0, 10)}. ${flag.stale!.suggestion}`;
        item.iconPath = new vscode.ThemeIcon("warning");
        item.contextValue = "staleFlag";
        return item;
      }
      case "use": {
        const { uri, use } = node;
        const item = new vscode.TreeItem(`${vscode.workspace.asRelativePath(uri)}:${use.line + 1}`);
        item.resourceUri = uri;
        item.iconPath = vscode.ThemeIcon.File;
        item.command = {
          command: "vscode.open",
          title: "Open",
          arguments: [uri, { selection: new vscode.Range(use.line, use.start, use.line, use.end) }],
        };
        return item;
      }
      case "none":
        return new vscode.TreeItem("Not used in this workspace");
    }
  }
}

async function usesIn(uri: vscode.Uri, keys: ReadonlySet<string>): Promise<Use[]> {
  try {
    if ((await vscode.workspace.fs.stat(uri)).size > maxFileBytes) return [];
    const bytes = await vscode.workspace.fs.readFile(uri);
    return looksLikeText(bytes) ? findUses(new TextDecoder().decode(bytes), keys) : [];
  } catch {
    return []; // deleted or unreadable since it was listed
  }
}

function plural(n: number, noun: string): string {
  return `${n} ${noun}${n === 1 ? "" : "s"}`;
}
