import { describe, expect, it } from "vitest";
import type { Flag } from "./api";
import { atLeast, envState, flagsHash, matchesSearch, parseRoute, validKey } from "./format";

describe("atLeast", () => {
  it("treats roles as cumulative", () => {
    expect(atLeast("admin", "editor")).toBe(true);
    expect(atLeast("approver", "editor")).toBe(true);
    expect(atLeast("editor", "editor")).toBe(true);
    expect(atLeast("viewer", "editor")).toBe(false);
  });
});

describe("envState", () => {
  it("matches stew list", () => {
    expect(envState(undefined)).toEqual({ label: "Off", kind: "off" });
    expect(envState({ enabled: false, rollout_percentage: 100, rules: [] })).toEqual({ label: "Off", kind: "off" });
    expect(envState({ enabled: true, rollout_percentage: 100, rules: [] })).toEqual({ label: "On", kind: "on" });
    expect(envState({ enabled: true, rollout_percentage: 25, rules: [] })).toEqual({ label: "25%", kind: "partial" });
    const rule = { attribute: "group" as const, values: ["staff"], serve: true };
    expect(envState({ enabled: true, rollout_percentage: 0, rules: [rule] }).label).toBe("0% +1 rule");
    expect(envState({ enabled: true, rollout_percentage: 100, rules: [rule, rule] }).label).toBe("On +2 rules");
  });
});

describe("matchesSearch", () => {
  const flag: Flag = {
    key: "new-checkout", name: "New checkout", description: "Faster payment page", steward: "sam",
    created_at: "", updated_at: "", environments: {},
  };
  it("matches key, name, description, and steward, ignoring case", () => {
    for (const q of ["", "  ", "CHECKOUT", "payment", "sam", "new-"]) expect(matchesSearch(flag, q)).toBe(true);
    expect(matchesSearch(flag, "dark")).toBe(false);
    expect(matchesSearch({ ...flag, steward: null }, "sam")).toBe(false);
  });
});

describe("validKey", () => {
  it("follows the server's rule", () => {
    for (const k of ["new-checkout", "a", "v2-api"]) expect(validKey(k)).toBe(true);
    for (const k of ["", "-lead", "Upper", "has space", "under_score", "a".repeat(101)]) expect(validKey(k)).toBe(false);
  });
});

describe("routes", () => {
  it("parses pages and filters", () => {
    expect(parseRoute("").page).toBe("flags");
    expect(parseRoute("#/flags/new").page).toBe("new-flag");
    expect(parseRoute("#/nope").page).toBe("not-found");
    const r = parseRoute("#/flags?env=prod&steward=mine&q=dark%20mode");
    expect(r.page === "flags" && Object.fromEntries(r.params)).toEqual({ env: "prod", steward: "mine", q: "dark mode" });
  });

  it("round-trips filters through the URL", () => {
    expect(flagsHash({ q: "", env: "", steward: "" })).toBe("#/flags");
    const hash = flagsHash({ q: "dark mode&x", env: "prod", steward: "none" });
    const r = parseRoute(hash);
    expect(r.page === "flags" && Object.fromEntries(r.params)).toEqual({ q: "dark mode&x", env: "prod", steward: "none" });
  });
});
