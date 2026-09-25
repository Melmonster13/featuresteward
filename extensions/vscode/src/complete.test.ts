import { describe, expect, it } from "vitest";
import { openStringAt } from "./complete";

// at splits a line at "|", the cursor.
function at(text: string) {
  const ch = text.indexOf("|");
  return openStringAt(text.replace("|", ""), ch);
}

describe("openStringAt", () => {
  it("finds an empty string just opened", () => {
    const empty = { start: 15, end: 15, prefix: "", text: "" };
    expect(at(`flags.enabled("|`)).toEqual(empty);
    expect(at(`flags.enabled('|')`)).toEqual(empty);
    expect(at("flags.enabled(`|`)")).toEqual(empty);
  });

  it("returns what's typed so far, and the rest of the key after the cursor", () => {
    expect(at(`enabled("new-ch|eckout")`)).toEqual({ start: 9, end: 21, prefix: "new-ch", text: "new-checkout" });
  });

  it("finds the second string on a line", () => {
    expect(at(`f("a", "b|`)?.prefix).toBe("b");
  });

  it.each([
    ["outside strings", `flags.enabled(|)`],
    ["after a closed string", `f("a")|`],
    ["between strings", `f("a", |"b")`],
    ["text that can't be a key", `log("Hello |`],
    ["a space in the text", `log("new checkout|`],
    ["after an escaped quote closes nothing", `f("a\\"b|`],
  ])("finds nothing %s", (_, text) => expect(at(text)).toBeUndefined());

  it("handles escaped quotes inside strings", () => {
    expect(at(`f("it\\"s", "|`)?.prefix).toBe("");
    expect(at(`f('don\\'t', 'da|`)?.prefix).toBe("da");
  });

  it("ignores other quote kinds inside a string", () => {
    expect(at(`f("it's ", '|`)?.prefix).toBe("");
    expect(at(`f('say "hi" ', "da|`)?.prefix).toBe("da");
  });
});
