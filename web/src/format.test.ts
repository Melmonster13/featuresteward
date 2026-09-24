import { describe, expect, it } from "vitest";
import type { AuditEvent, Flag } from "./api";
import {
  atLeast, describeEvent, expiryDate, lockReason, tokenStatus, userHash, envState, flagHash, flagsHash, matchesSearch, parseRoute, parseValues, sameConfig, validKey,
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

describe("admin routes", () => {
  it("parses admin pages and user handles", () => {
    expect(parseRoute("#/tokens").page).toBe("tokens");
    expect(parseRoute("#/admin/users").page).toBe("users");
    expect(parseRoute("#/admin/sdk-keys").page).toBe("sdk-keys");
    expect(parseRoute("#/admin/environments").page).toBe("environments");
    expect(parseRoute(userHash("sam.lee"))).toEqual({ page: "user", handle: "sam.lee" });
    for (const bad of ["#/admin/users/Sam", "#/admin/users/", "#/admin/users/a%20b", "#/admin/nope"]) {
      expect(parseRoute(bad).page).toBe("not-found");
    }
  });
});

describe("tokens", () => {
  const now = new Date("2026-09-24T12:00:00Z");
  it("computes expiry dates", () => {
    expect(expiryDate(0, now)).toBeUndefined();
    expect(expiryDate(30, now)).toBe("2026-10-24T12:00:00.000Z");
  });
  it("reports status", () => {
    expect(tokenStatus({}, now)).toBe("active");
    expect(tokenStatus({ expires_at: "2026-10-01T00:00:00Z" }, now)).toBe("active");
    expect(tokenStatus({ expires_at: "2026-09-01T00:00:00Z" }, now)).toBe("expired");
    expect(tokenStatus({ revoked_at: "2026-09-02T00:00:00Z", expires_at: "2026-09-01T00:00:00Z" }, now)).toBe("revoked");
  });
  it("describes user and token events", () => {
    const env = (k: string) => k;
    const ev = (action: string, before: unknown, after: unknown): AuditEvent => ({ id: 1, occurred_at: "", actor: "mel", action, before, after });
    expect(describeEvent(ev("user.created", null, { handle: "sam", role: "editor" }), env)).toBe("created the user as editor");
    expect(describeEvent(ev("user.role_changed", { role: "editor" }, { role: "admin" }), env)).toBe("changed the role from editor to admin");
    expect(describeEvent(ev("token.created", null, { id: 3, name: "laptop", prefix: "fs_1a2b3c4d" }), env)).toBe(
      "created token “laptop” (fs_1a2b3c4d…)",
    );
    expect(describeEvent(ev("token.revoked", { id: 3, name: "laptop", prefix: "fs_1a2b3c4d" }, null), env)).toBe(
      "revoked token “laptop” (fs_1a2b3c4d…)",
    );
  });
});
