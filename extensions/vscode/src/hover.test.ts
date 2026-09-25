import { describe, expect, it } from "vitest";
import type { EnvConfig, Environment, Flag } from "./api";
import { envState, escapeMarkdown, hoverMarkdown, keyAt } from "./hover";

describe("keyAt", () => {
  const line = `if (flags.enabled("new-checkout", user) && x === 'beta') {`;

  it("finds the key under the cursor, including on its quotes", () => {
    const start = line.indexOf("new-checkout");
    for (const ch of [start - 1, start, start + 5, start + "new-checkout".length]) {
      expect(keyAt(line, ch), `column ${ch}`).toEqual({ key: "new-checkout", start, end: start + 12 });
    }
  });

  it("finds keys in single quotes and backticks", () => {
    expect(keyAt(line, line.indexOf("beta"))?.key).toBe("beta");
    expect(keyAt("check(`dark-mode`)", 8)?.key).toBe("dark-mode");
  });

  it.each([
    ["outside strings", line.indexOf("flags")],
    ["between strings", line.indexOf("user")],
    ["just after the closing quote", line.indexOf(`", user`) + 1],
    ["just before the opening quote", line.indexOf(`"new`) - 1],
  ])("finds nothing %s", (_, ch) => expect(keyAt(line, ch)).toBeUndefined());

  it("ignores strings that can't be keys", () => {
    for (const s of [`"New-Checkout"`, `"new checkout"`, `"-x"`, `"a_b"`, `""`, `"${"a".repeat(101)}"`]) {
      expect(keyAt(s, 2), s).toBeUndefined();
    }
  });

  it("doesn't pair quotes of different kinds", () => {
    expect(keyAt(`"abc'`, 2)).toBeUndefined();
  });
});

const cfg = (enabled: boolean, rollout_percentage = 100, rules = 0): EnvConfig => ({
  enabled,
  rollout_percentage,
  rules: Array.from({ length: rules }, () => ({ attribute: "group" as const, values: ["staff"], serve: true })),
});

describe("envState", () => {
  it.each([
    [cfg(false), "Off"],
    [cfg(true), "On"],
    [cfg(true, 25), "25%"],
    [cfg(true, 100, 2), "On +2 rules"],
    [cfg(true, 0, 1), "0% +1 rule"],
    [cfg(false, 30), "Off (30% when on)"],
  ])("%j is %s", (c, want) => expect(envState(c)).toBe(want));
});

describe("escapeMarkdown", () => {
  it("neutralizes links, images, HTML, icons, and formatting", () => {
    const out = escapeMarkdown("[click](command:workbench.action.terminal.new) ![x](http://e/x.png) <img src=x> $(alert) **b** `c`");
    expect(out).not.toMatch(/(^|[^\\])[[\]()<>`*]/);
  });

  it("keeps text on one line", () => {
    expect(escapeMarkdown("a\n\n# heading\n| x |")).toBe("a \\# heading \\| x \\|");
  });
});

const envs: Environment[] = [
  { key: "dev", name: "Development", protected: false },
  { key: "prod", name: "Production", protected: true },
];

function flag(overrides: Partial<Flag> = {}): Flag {
  return {
    key: "new-checkout",
    name: "New checkout",
    description: "The redesigned checkout.",
    steward: "sam",
    environments: { dev: cfg(true), prod: cfg(false, 25) },
    permanent_reason: null,
    stale: null,
    ...overrides,
  };
}

describe("hoverMarkdown", () => {
  it("describes the flag", () => {
    expect(hoverMarkdown(flag(), envs, "https://flags.example.com/")).toBe(
      [
        "**New checkout** `new-checkout`",
        "",
        "The redesigned checkout\\.",
        "",
        "| Environment | State |",
        "|---|---|",
        "| Development | On |",
        "| Production | Off \\(25% when on\\) |",
        "",
        "Steward: @sam",
        "",
        "[Open in dashboard](https://flags.example.com/#/flags/new-checkout)",
      ].join("\n"),
    );
  });

  it("shows stale and permanent flags, and no steward", () => {
    const md = hoverMarkdown(
      flag({
        steward: null,
        stale: { reason: "always_on", since: "2026-09-15T10:00:00Z", suggestion: "Remove the flag." },
        permanent_reason: "ops kill switch",
      }),
      envs,
      "http://localhost:8080",
    );
    expect(md).toContain("Steward: none");
    expect(md).toContain("$(warning) **Stale: Always on** since 2026\\-09\\-15. Remove the flag\\.");
    expect(md).toContain("Permanent: ops kill switch");
  });

  it("lists environments the list doesn't know by key", () => {
    expect(hoverMarkdown(flag({ environments: { qa: cfg(true) } }), envs, "https://x")).toContain("| qa | On |");
  });

  it("escapes everything other users wrote", () => {
    const evil = "[x](command:evil) <script>";
    const md = hoverMarkdown(
      flag({ name: evil, description: evil, permanent_reason: evil, stale: { reason: "unused", since: evil, suggestion: evil } }),
      [{ key: "dev", name: evil, protected: false }],
      "https://x",
    );
    expect(md).not.toContain("](command:");
    expect(md).not.toContain("<script>");
    expect(md.match(/\]\(/g)).toHaveLength(1); // only the dashboard link
  });

  it("clips long text", () => {
    const md = hoverMarkdown(flag({ description: "x".repeat(1000) }), envs, "https://x");
    expect(md).toContain(`${"x".repeat(299)}…`);
    expect(md).not.toContain("x".repeat(300));
  });
});
