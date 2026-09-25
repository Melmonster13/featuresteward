import * as vscode from "vscode";
import { APIError, checkToken, checkURL, Client, type Environment, type Flag } from "./api";
import { openStringAt } from "./complete";
import { hoverMarkdown, keyAt, staleLabel } from "./hover";

// The token is kept in VS Code's secret storage together with the URL it
// was issued for, so changing the URL setting never sends it elsewhere.
const secretKey = "featuresteward.session";

interface Session {
  url: string;
  token: string;
}

let status: vscode.StatusBarItem;
let secrets: vscode.SecretStorage;
let log: vscode.LogOutputChannel;
// refreshes counts refresh calls, so only the latest one updates the status.
let refreshes = 0;
// The flags and environments from the last successful refresh. They're
// kept through network errors, and cleared on signing out.
let flags = new Map<string, Flag>();
let environments: Environment[] = [];

const refreshEvery = 60_000;
const documents: vscode.DocumentSelector = [{ scheme: "file" }, { scheme: "untitled" }];

export function activate(context: vscode.ExtensionContext): void {
  secrets = context.secrets;
  status = vscode.window.createStatusBarItem(vscode.StatusBarAlignment.Left, 50);
  status.name = "FeatureSteward";
  status.show();
  log = vscode.window.createOutputChannel("FeatureSteward", { log: true });
  context.subscriptions.push(
    status,
    log,
    vscode.commands.registerCommand("featuresteward.signIn", signIn),
    vscode.commands.registerCommand("featuresteward.signOut", signOut),
    vscode.commands.registerCommand("featuresteward.refresh", refresh),
    vscode.languages.registerHoverProvider(documents, { provideHover }),
    vscode.languages.registerCompletionItemProvider(documents, { provideCompletionItems }, '"', "'", "`"),
    vscode.workspace.onDidChangeConfiguration((e) => {
      if (e.affectsConfiguration("featuresteward.url")) void refresh();
    }),
    secrets.onDidChange((e) => {
      if (e.key === secretKey) void refresh();
    }),
  );
  const timer = setInterval(() => void refresh(), refreshEvery);
  context.subscriptions.push({ dispose: () => clearInterval(timer) });
  void refresh();
}

export function deactivate(): void {}

function configuredURL(): string {
  return (vscode.workspace.getConfiguration("featuresteward").get<string>("url") ?? "").trim();
}

// client returns a client for the configured server, or undefined when
// signed out or the URL changed since signing in.
async function client(): Promise<Client | undefined> {
  const raw = await secrets.get(secretKey);
  if (!raw) {
    log.info("Not signed in.");
    return undefined;
  }
  let s: Session;
  try {
    s = JSON.parse(raw) as Session;
  } catch {
    log.warn("The saved session is unreadable; sign in again.");
    return undefined;
  }
  const url = configuredURL();
  if (s.url !== url || checkURL(url)) {
    log.info(`Signed out: the token was saved for ${s.url}, but the URL setting is ${url || "empty"}.`);
    return undefined;
  }
  return new Client(s.url, s.token);
}

async function refresh(): Promise<void> {
  const id = ++refreshes;
  const c = await client();
  if (id !== refreshes) return;
  if (!c) {
    flags = new Map();
    environments = [];
    show("$(flag) FeatureSteward: signed out", "Sign in to see your flags", "featuresteward.signIn");
    return;
  }
  try {
    const [list, envs] = await Promise.all([c.flags(), c.environments()]);
    if (id !== refreshes) return;
    if (list.length !== flags.size) log.info(`Loaded ${list.length} flags from ${configuredURL()}.`);
    flags = new Map(list.map((f) => [f.key, f]));
    environments = envs;
    show(`$(flag) ${list.length} flag${list.length === 1 ? "" : "s"}`, `FeatureSteward at ${configuredURL()}. Click to refresh.`, "featuresteward.refresh");
  } catch (err) {
    if (id !== refreshes) return;
    log.error(`Loading flags failed: ${message(err)}`);
    const signIn = err instanceof APIError && err.status === 401;
    show("$(warning) FeatureSteward", message(err), signIn ? "featuresteward.signIn" : undefined);
  }
}

function provideHover(doc: vscode.TextDocument, pos: vscode.Position): vscode.Hover | undefined {
  const m = keyAt(doc.lineAt(pos.line).text, pos.character);
  const flag = m && flags.get(m.key);
  if (!m || !flag) return undefined;
  return new vscode.Hover(describe(flag), new vscode.Range(pos.line, m.start, pos.line, m.end));
}

function provideCompletionItems(doc: vscode.TextDocument, pos: vscode.Position): vscode.CompletionItem[] | undefined {
  if (!vscode.workspace.getConfiguration("featuresteward", doc).get<boolean>("completion", true)) return undefined;
  const open = openStringAt(doc.lineAt(pos.line).text, pos.character);
  if (!open || flags.size === 0) return undefined;
  const range = new vscode.Range(pos.line, open.start, pos.line, open.end);
  return [...flags.values()].map((f) => {
    const item = new vscode.CompletionItem({ label: f.key, description: f.name }, vscode.CompletionItemKind.Constant);
    item.range = range;
    item.detail = f.steward ? `Steward: @${f.steward}` : "No steward";
    item.documentation = describe(f);
    if (f.stale) {
      item.tags = [vscode.CompletionItemTag.Deprecated];
      item.detail += ` · Stale: ${staleLabel(f.stale.reason)}`;
    }
    // Stale flags sort last.
    item.sortText = `${f.stale ? 1 : 0}${f.key}`;
    return item;
  });
}

// describe renders a flag's details. Not trusted: links can't run
// commands, and HTML isn't rendered.
function describe(flag: Flag): vscode.MarkdownString {
  const md = new vscode.MarkdownString(hoverMarkdown(flag, environments, configuredURL()), true);
  md.isTrusted = false;
  md.supportHtml = false;
  return md;
}

function show(text: string, tooltip: string, command?: string): void {
  status.text = text;
  status.tooltip = tooltip;
  status.command = command;
}

async function signIn(): Promise<void> {
  let url = configuredURL();
  if (!url || checkURL(url)) {
    const entered = await vscode.window.showInputBox({
      title: "FeatureSteward server",
      prompt: "The URL of your FeatureSteward server",
      placeHolder: "https://flags.example.com",
      value: url,
      ignoreFocusOut: true,
      validateInput: (v) => checkURL(v.trim()) || undefined,
    });
    if (entered === undefined) return;
    url = entered.trim().replace(/\/+$/, "");
  }
  const token = await vscode.window.showInputBox({
    title: "FeatureSteward API token",
    prompt: "Paste an API token. The extension only reads, so a viewer token is enough.",
    placeHolder: "fs_…",
    password: true,
    ignoreFocusOut: true,
    validateInput: (v) => checkToken(v) || undefined,
  });
  if (token === undefined) return;

  let handle: string;
  try {
    handle = (await new Client(url, token.trim()).me()).handle;
  } catch (err) {
    log.error(`Sign-in failed: ${message(err)}`);
    void vscode.window.showErrorMessage(`FeatureSteward: ${message(err)}`);
    return;
  }
  // Save the URL first, so the session below matches it when refresh runs.
  if (url !== configuredURL()) {
    await vscode.workspace.getConfiguration("featuresteward").update("url", url, vscode.ConfigurationTarget.Global);
  }
  await secrets.store(secretKey, JSON.stringify({ url, token: token.trim() } satisfies Session));
  log.info(`Signed in as @${handle} at ${url}.`);
  void vscode.window.showInformationMessage(`FeatureSteward: signed in as @${handle}.`);
  await refresh();
}

async function signOut(): Promise<void> {
  await secrets.delete(secretKey);
  log.info("Signed out.");
  void vscode.window.showInformationMessage(
    "FeatureSteward: signed out. The token still works until you revoke it on the dashboard's Your tokens page.",
  );
}

function message(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}
