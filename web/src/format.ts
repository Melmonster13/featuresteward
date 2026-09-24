import type { EnvConfig, Flag, Role } from "./api";

const rank: Record<Role, number> = { viewer: 1, editor: 2, approver: 3, admin: 4 };

// atLeast mirrors the server's cumulative roles. The server still
// enforces every permission; this only hides controls that would fail.
export function atLeast(role: Role, min: Role): boolean {
  return rank[role] >= rank[min];
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

export type Route = { page: "flags"; params: URLSearchParams } | { page: "new-flag" } | { page: "not-found" };

export function parseRoute(hash: string): Route {
  const [path, query = ""] = hash.replace(/^#/, "").split("?", 2);
  switch (path) {
    case "":
    case "/":
    case "/flags":
      return { page: "flags", params: new URLSearchParams(query) };
    case "/flags/new":
      return { page: "new-flag" };
    default:
      return { page: "not-found" };
  }
}

// flagsHash builds the list page's URL, leaving out empty filters.
export function flagsHash(filters: { q: string; env: string; steward: string }): string {
  const params = new URLSearchParams();
  for (const [k, v] of Object.entries(filters)) if (v) params.set(k, v);
  const query = params.toString();
  return query ? `#/flags?${query}` : "#/flags";
}
