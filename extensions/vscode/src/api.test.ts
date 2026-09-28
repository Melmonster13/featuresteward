import { describe, expect, it } from "vitest";
import { APIError, checkToken, checkURL, Client, plainText, validHandle } from "./api";

describe("checkURL", () => {
  it.each(["https://flags.example.com", "https://flags.example.com:8443/", "http://localhost:8080", "http://127.0.0.1:8080", "http://[::1]:8080"])(
    "accepts %s",
    (url) => expect(checkURL(url)).toBe(""),
  );

  it.each([
    ["", "Enter a URL"],
    ["flags.example.com", "Enter a URL"],
    ["ftp://flags.example.com", "must start with https"],
    ["http://flags.example.com", "only allowed to localhost"],
    ["http://localhost.example.com", "only allowed to localhost"],
    ["https://user:pw@flags.example.com", "user name or password"],
  ])("rejects %s", (url, want) => expect(checkURL(url)).toContain(want));
});

describe("checkToken", () => {
  it("accepts API tokens", () => expect(checkToken(" fs_abc123_XYZ-9 ")).toBe(""));
  it("rejects SDK keys", () => expect(checkToken("fs_sdk_abc")).toContain("SDK key"));
  it.each(["", "abc", "fs_", "fs_a b"])("rejects %j", (t) => expect(checkToken(t)).toContain("start with fs_"));
});

function fakeFetch(status: number, body: unknown, seen: { url?: string; init?: RequestInit } = {}): typeof fetch {
  return (async (url: string, init?: RequestInit) => {
    seen.url = url;
    seen.init = init;
    return new Response(typeof body === "string" ? body : JSON.stringify(body), { status });
  }) as typeof fetch;
}

describe("Client", () => {
  it("sends the token and refuses redirects", async () => {
    const seen: { url?: string; init?: RequestInit } = {};
    const flags = await new Client("https://flags.example.com/", "fs_secret", fakeFetch(200, { flags: [{ key: "a" }] }, seen)).flags();
    expect(flags).toEqual([{ key: "a" }]);
    expect(seen.url).toBe("https://flags.example.com/api/v1/flags");
    expect((seen.init?.headers as Record<string, string>).Authorization).toBe("Bearer fs_secret");
    expect(seen.init?.redirect).toBe("error");
  });

  it("returns no flags for a null list", async () => {
    expect(await new Client("https://x", "fs_t", fakeFetch(200, { flags: null })).flags()).toEqual([]);
  });

  it.each([
    [401, { error: "unauthorized" }, "rejected the token"],
    [429, "", "Rate limited"],
    [403, { error: "forbidden" }, "forbidden"],
    [500, "<html>oops</html>", "returned 500"],
  ])("explains a %i", async (status, body, want) => {
    const err = await new Client("https://x", "fs_t", fakeFetch(status, body)).me().catch((e: unknown) => e);
    expect(err).toBeInstanceOf(APIError);
    expect((err as APIError).status).toBe(status);
    expect((err as APIError).message).toContain(want);
  });

  it("never puts the token in an error", async () => {
    const failing = (async () => {
      throw new TypeError("fetch failed: fs_secret");
    }) as typeof fetch;
    const err = (await new Client("https://x", "fs_secret", failing).me().catch((e: unknown) => e)) as Error;
    expect(err.message).toBe("Couldn't reach https://x");
  });
});

describe("plainText", () => {
  it("removes link and HTML syntax from server text", () => {
    const out = plainText("Denied. [Fix it](command:workbench.action.terminal.new) <img src=x> `code`");
    expect(out).not.toMatch(/[[\]<>`]/);
    expect(out).toContain("Denied.");
  });
  it("keeps it short and on one line", () => expect(plainText(`a\n\nb ${"x".repeat(500)}`)).toHaveLength(200));
});

describe("validHandle", () => {
  it.each(["sam", "melmel6", "a.b_c-d"])("accepts %s", (h) => expect(validHandle(h)).toBe(true));
  it.each(["", "Sam", "[x](command:y)", "a b", 42, null])("rejects %j", (h) => expect(validHandle(h)).toBe(false));
});

it("strips links from server error messages", async () => {
  const err = (await new Client("https://x", "fs_t", fakeFetch(403, { error: "[Click](command:evil)" })).me().catch((e: unknown) => e)) as Error;
  expect(err.message).toBe("Click(command:evil)");
});
