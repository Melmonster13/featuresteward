import * as vscode from "vscode";
import { APIError, checkToken, checkURL, Client } from "./api";

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
    vscode.workspace.onDidChangeConfiguration((e) => {
      if (e.affectsConfiguration("featuresteward.url")) void refresh();
    }),
    secrets.onDidChange((e) => {
      if (e.key === secretKey) void refresh();
    }),
  );
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
    show("$(flag) FeatureSteward: signed out", "Sign in to see your flags", "featuresteward.signIn");
    return;
  }
  try {
    const flags = await c.flags();
    if (id !== refreshes) return;
    log.info(`Loaded ${flags.length} flags from ${configuredURL()}.`);
    show(`$(flag) ${flags.length} flag${flags.length === 1 ? "" : "s"}`, `FeatureSteward at ${configuredURL()}`);
  } catch (err) {
    if (id !== refreshes) return;
    log.error(`Loading flags failed: ${message(err)}`);
    const signIn = err instanceof APIError && err.status === 401;
    show("$(warning) FeatureSteward", message(err), signIn ? "featuresteward.signIn" : undefined);
  }
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
