import { describe, expect, it } from "vitest";
import type { AuditEvent, Flag } from "./api";
import {
  atLeast, describeEvent, lockReason, envState, flagHash, flagsHash, matchesSearch, parseRoute, parseValues, sameConfig, validKey,
} from "./format";

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
    expect(parseRoute("#/flags/new-checkout")).toEqual({ page: "flag", key: "new-checkout" });
    expect(parseRoute(flagHash("dark-mode"))).toEqual({ page: "flag", key: "dark-mode" });
    for (const bad of ["#/flags/Bad Key", "#/flags/%E0%A4%A", "#/flags/", "#/flags/a/b"]) {
      expect(parseRoute(bad).page).toBe("not-found");
    }
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

describe("parseValues", () => {
  it("splits on commas and drops blanks", () => {
    expect(parseValues(" staff, beta ,,  qa ")).toEqual(["staff", "beta", "qa"]);
    expect(parseValues(" , ")).toEqual([]);
  });
});

describe("sameConfig", () => {
  const base = { enabled: true, rollout_percentage: 25, rules: [{ attribute: "group" as const, values: ["staff"], serve: true }] };
  it("compares every field, including rule order and values", () => {
    expect(sameConfig(base, structuredClone(base))).toBe(true);
    expect(sameConfig(base, { ...base, enabled: false })).toBe(false);
    expect(sameConfig(base, { ...base, rollout_percentage: 30 })).toBe(false);
    expect(sameConfig(base, { ...base, rules: [{ ...base.rules[0], values: ["staff", "qa"] }] })).toBe(false);
    expect(sameConfig(base, { ...base, rules: [{ ...base.rules[0], serve: false }] })).toBe(false);
    expect(sameConfig({ ...base, rules: [] }, { ...base, rules: [] })).toBe(true);
  });
});

describe("describeEvent", () => {
  const env = (k: string) => ({ prod: "Production" })[k] ?? k;
  const event = (action: string, before: unknown, after: unknown, environment?: string): AuditEvent => ({
    id: 1, occurred_at: "", actor: "mel", action, environment, before, after,
  });
  it("describes each kind of change", () => {
    expect(describeEvent(event("flag.created", null, { key: "x" }), env)).toBe("created the flag");
    expect(
      describeEvent(
        event("flag.environment_updated", { enabled: false, rollout_percentage: 100, rules: [] },
          { enabled: true, rollout_percentage: 25, rules: [] }, "prod"),
        env,
      ),
    ).toBe("changed Production from Off to 25%");
    const rule = { attribute: "group", values: ["staff"], serve: true };
    expect(
      describeEvent(
        event("flag.environment_updated", { enabled: true, rollout_percentage: 25, rules: [rule] },
          { enabled: true, rollout_percentage: 25, rules: [{ ...rule, values: ["qa"] }] }, "prod"),
        env,
      ),
    ).toBe("changed the targeting rules in Production");
    expect(describeEvent(event("flag.steward_changed", { steward: "mel" }, { steward: "sam" }), env)).toBe(
      "changed the steward from @mel to @sam",
    );
    expect(describeEvent(event("flag.steward_changed", { steward: null }, { steward: "sam" }), env)).toBe(
      "changed the steward from nobody to @sam",
    );
    expect(describeEvent(event("flag.updated", { name: "A", description: "" }, { name: "B", description: "d" }), env)).toBe(
      "renamed it from “A” to “B” and changed the description",
    );
    expect(describeEvent(event("flag.archived", null, null), env)).toBe("archived the flag");
    expect(describeEvent(event("something.new", null, null), env)).toBe("something.new");
  });
});

describe("lockReason", () => {
  const dev = { key: "dev", name: "Development", protected: false };
  const prod = { key: "prod", name: "Production", protected: true };
  it("matches the server's permissions", () => {
    expect(lockReason("viewer", dev, false)).toBe("Changing flags needs the editor role.");
    expect(lockReason("editor", dev, false)).toBe("");
    expect(lockReason("editor", prod, false)).toBe("Changes to Production need an admin until approvals are available.");
    expect(lockReason("approver", prod, false)).toMatch(/need an admin/);
    expect(lockReason("admin", prod, false)).toBe("");
    expect(lockReason("admin", dev, true)).toBe("Archived flags can't be changed.");
  });
});
