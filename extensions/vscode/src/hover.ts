// Finding flag keys in code and describing flags. No VS Code
// dependencies, so it's tested on its own.
import type { EnvConfig, Environment, Flag, StaleReason } from "./api";
import { quoted } from "./scan";

export interface KeyMatch {
  key: string;
  // start and end are the key's columns, without the quotes.
  start: number;
  end: number;
}


// keyAt returns the quoted flag-key-shaped string at column ch of line,
// if any. The caller checks that it's a known key.
export function keyAt(line: string, ch: number): KeyMatch | undefined {
  for (const m of line.matchAll(quoted)) {
    const start = m.index + 1;
    const end = start + m[2].length;
    if (ch >= m.index && ch <= end) return { key: m[2], start, end };
  }
  return undefined;
}

// envState summarizes a flag in one environment, like the dashboard:
// "Off", "On", "25%", "On +1 rule", or "Off (25% when on)".
export function envState(cfg: EnvConfig): string {
  let on = cfg.rollout_percentage < 100 ? `${cfg.rollout_percentage}%` : "On";
  const rules = cfg.rules?.length ?? 0;
  if (rules > 0) on += ` +${rules} rule${rules > 1 ? "s" : ""}`;
  if (cfg.enabled) return on;
  return on === "On" ? "Off" : `Off (${on} when on)`;
}

export function staleLabel(reason: StaleReason): string {
  return { unused: "Unused", always_on: "Always on", always_off: "Always off", settled_mixed: "Settled" }[reason] ?? reason;
}

// escapeMarkdown makes untrusted text (flag names, descriptions, reasons)
// render as plain text: no links, images, HTML, or formatting.
export function escapeMarkdown(text: string): string {
  return text
    .replace(/[\\`*_{}[\]()#+\-.!|~<>&:]/g, "\\$&")
    .replace(/\s+/g, " ")
    .trim();
}

function clip(text: string, max: number): string {
  return text.length > max ? `${text.slice(0, max - 1)}…` : text;
}

// hoverMarkdown describes a flag for a hover. dashboard is the server URL.
// Every value from the server is escaped, since other users write them.
export function hoverMarkdown(flag: Flag, envs: Environment[], dashboard: string): string {
  const lines = [`**${escapeMarkdown(clip(flag.name, 100))}** \`${flag.key}\``];
  if (flag.description) lines.push("", escapeMarkdown(clip(flag.description, 300)));

  lines.push("", "| Environment | State |", "|---|---|");
  const known = new Set(envs.map((e) => e.key));
  const rows = [...envs.map((e) => ({ key: e.key, name: e.name })), ...Object.keys(flag.environments).filter((k) => !known.has(k)).map((k) => ({ key: k, name: k }))];
  for (const e of rows) {
    const cfg = flag.environments[e.key];
    if (cfg) lines.push(`| ${escapeMarkdown(e.name)} | ${escapeMarkdown(envState(cfg))} |`);
  }

  lines.push("", `Steward: ${flag.steward ? escapeMarkdown(`@${flag.steward}`) : "none"}`);
  if (flag.stale) {
    const since = flag.stale.since.slice(0, 10);
    lines.push("", `$(warning) **Stale: ${staleLabel(flag.stale.reason)}** since ${escapeMarkdown(since)}. ${escapeMarkdown(flag.stale.suggestion)}`);
  }
  if (flag.permanent_reason) lines.push("", `Permanent: ${escapeMarkdown(clip(flag.permanent_reason, 200))}`);
  lines.push("", `[Open in dashboard](${dashboard.replace(/\/+$/, "")}/#/flags/${encodeURIComponent(flag.key)})`);
  return lines.join("\n");
}
