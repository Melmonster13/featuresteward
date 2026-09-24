import type { AuditEvent, EnvConfig, Environment, Flag, Role, Rule } from "./api";

const rank: Record<Role, number> = { viewer: 1, editor: 2, approver: 3, admin: 4 };

// atLeast mirrors the server's cumulative roles. The server still
// enforces every permission; this only hides controls that would fail.
export function atLeast(role: Role, min: Role): boolean {
  return rank[role] >= rank[min];
}

// lockReason says why the user can't change a flag in env, or "" if
// they can. It mirrors the server, which enforces the same rules.
export function lockReason(role: Role, env: Environment, archived: boolean): string {
  if (archived) return "Archived flags can't be changed.";
  if (!atLeast(role, "editor")) return "Changing flags needs the editor role.";
  if (env.protected && !atLeast(role, "admin")) return `Changes to ${env.name} need an admin until approvals are available.`;
  return "";
}

// envState summarizes a flag in one environment, like `stew list`.
export function envState(cfg: EnvConfig | undefined): { label: string; kind: "off" | "on" | "partial" } {
  if (!cfg || !cfg.enabled) return { label: "Off", kind: "off" };
  let label = cfg.rollout_percentage < 100 ? `${cfg.rollout_percentage}%` : "On";
  const rules = cfg.rules?.length ?? 0;
  if (rules > 0) label += ` +${rules} rule${rules > 1 ? "s" : ""}`;
  return { label, kind: cfg.rollout_percentage < 100 ? "partial" : "on" };
}

export function matchesSearch(flag: Flag, query: string): boolean {
  const q = query.trim().toLowerCase();
  if (!q) return true;
  return [flag.key, flag.name, flag.description, flag.steward ?? ""].some((s) => s.toLowerCase().includes(q));
}

export function validKey(key: string): boolean {
  return key.length <= 100 && /^[a-z0-9][a-z0-9-]*$/.test(key);
}

export type Route =
  | { page: "flags"; params: URLSearchParams }
  | { page: "new-flag" }
  | { page: "flag"; key: string }
  | { page: "tokens" }
  | { page: "users" }
  | { page: "user"; handle: string }
  | { page: "sdk-keys" }
  | { page: "environments" }
  | { page: "not-found" };

export function parseRoute(hash: string): Route {
  const [path, query = ""] = hash.replace(/^#/, "").split("?", 2);
  switch (path) {
    case "":
    case "/":
    case "/flags":
      return { page: "flags", params: new URLSearchParams(query) };
    case "/flags/new":
      return { page: "new-flag" };
    case "/tokens":
      return { page: "tokens" };
    case "/admin/users":
      return { page: "users" };
    case "/admin/sdk-keys":
      return { page: "sdk-keys" };
    case "/admin/environments":
      return { page: "environments" };
  }
  if (path.startsWith("/admin/users/")) {
    const handle = safeDecode(path.slice("/admin/users/".length));
    return validHandle(handle) ? { page: "user", handle } : { page: "not-found" };
  }
  const key = path.startsWith("/flags/") ? safeDecode(path.slice("/flags/".length)) : "";
  return key && validKey(key) ? { page: "flag", key } : { page: "not-found" };
}

export function validHandle(handle: string): boolean {
  return /^[a-z0-9][a-z0-9._-]{0,63}$/.test(handle);
}

export function userHash(handle: string): string {
  return `#/admin/users/${encodeURIComponent(handle)}`;
}

// expiryDate returns the ISO time `days` from now, or undefined for 0
// (never expires).
export function expiryDate(days: number, now = new Date()): string | undefined {
  return days > 0 ? new Date(now.getTime() + days * 86_400_000).toISOString() : undefined;
}

// tokenStatus says whether a token or key still works.
export function tokenStatus(t: { revoked_at?: string; expires_at?: string }, now = new Date()): "active" | "revoked" | "expired" {
  if (t.revoked_at) return "revoked";
  if (t.expires_at && new Date(t.expires_at) <= now) return "expired";
  return "active";
}

export function flagHash(key: string): string {
  return `#/flags/${encodeURIComponent(key)}`;
}

function safeDecode(s: string): string {
  try {
    return decodeURIComponent(s);
  } catch {
    return "";
  }
}

// flagsHash builds the list page's URL, leaving out empty filters.
export function flagsHash(filters: { q: string; env: string; steward: string }): string {
  const params = new URLSearchParams();
  for (const [k, v] of Object.entries(filters)) if (v) params.set(k, v);
  const query = params.toString();
  return query ? `#/flags?${query}` : "#/flags";
}

// parseValues splits a comma-separated list, dropping blanks.
export function parseValues(text: string): string[] {
  return text
    .split(",")
    .map((v) => v.trim())
    .filter(Boolean);
}

export function sameConfig(a: EnvConfig, b: EnvConfig): boolean {
  return a.enabled === b.enabled && a.rollout_percentage === b.rollout_percentage && sameRules(a.rules ?? [], b.rules ?? []);
}

function sameRules(a: Rule[], b: Rule[]): boolean {
  return JSON.stringify(a.map(ruleKey)) === JSON.stringify(b.map(ruleKey));
}

function ruleKey(r: Rule): unknown[] {
  return [r.attribute, r.serve, r.values];
}

// describeEvent turns an audit event into a sentence, e.g.
// "changed Production from Off to 25% +1 rule".
export function describeEvent(e: AuditEvent, envName: (key: string) => string): string {
  const before = (e.before ?? {}) as Record<string, unknown>;
  const after = (e.after ?? {}) as Record<string, unknown>;
  switch (e.action) {
    case "flag.created":
      return "created the flag";
    case "flag.updated": {
      const changes: string[] = [];
      if (before.name !== after.name) changes.push(`renamed it from “${before.name}” to “${after.name}”`);
      if (before.description !== after.description) changes.push("changed the description");
      return changes.join(" and ") || "updated the flag";
    }
    case "flag.environment_updated": {
      const name = envName(e.environment ?? "");
      const from = envState(before as unknown as EnvConfig).label;
      const to = envState(after as unknown as EnvConfig).label;
      if (from !== to) return `changed ${name} from ${from} to ${to}`;
      return `changed the targeting rules in ${name}`;
    }
    case "flag.steward_changed":
      return `changed the steward from ${handle(before.steward)} to ${handle(after.steward)}`;
    case "flag.archived":
      return "archived the flag";
    case "user.created":
      return `created the user as ${after.role}`;
    case "user.role_changed":
      return `changed the role from ${before.role} to ${after.role}`;
    case "user.disabled":
      return "disabled the user and revoked their tokens";
    case "token.created":
      return `created token “${after.name}” (${after.prefix}…)`;
    case "token.revoked":
      return `revoked token “${before.name}” (${before.prefix}…)`;
    default:
      return e.action;
  }
}

function handle(v: unknown): string {
  return typeof v === "string" && v ? `@${v}` : "nobody";
}
