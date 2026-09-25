import { describe, expect, it } from "vitest";
import { findUses, looksLikeText, maxFileBytes } from "./scan";

describe("findUses", () => {
  const keys = new Set(["legacy-search", "dark-mode"]);

  it("finds every quoted use of the keys, with positions", () => {
    const text = [
      `if (flags.enabled("legacy-search")) search();`,
      `const a = 'dark-mode', b = \`dark-mode\`;`,
      "",
      `x("legacy-search-v2", "new-checkout")`,
    ].join("\r\n");
    expect(findUses(text, keys)).toEqual([
      { key: "legacy-search", line: 0, start: 19, end: 32 },
      { key: "dark-mode", line: 1, start: 11, end: 20 },
      { key: "dark-mode", line: 1, start: 28, end: 37 },
    ]);
  });

  it("ignores unquoted and mismatched mentions", () => {
    expect(findUses(`// legacy-search is old\nf("dark-mode')`, keys)).toEqual([]);
  });

  it("finds nothing when there are no keys", () => {
    expect(findUses(`"legacy-search"`, new Set())).toEqual([]);
  });
});

describe("looksLikeText", () => {
  it("accepts source code", () => expect(looksLikeText(new TextEncoder().encode("const a = 1;\n"))).toBe(true));
  it("rejects binary files", () => expect(looksLikeText(new Uint8Array([0x89, 0x50, 0x4e, 0x47, 0, 1]))).toBe(false));
  it("rejects huge files", () => expect(looksLikeText(new Uint8Array(maxFileBytes + 1).fill(0x61))).toBe(false));
});
