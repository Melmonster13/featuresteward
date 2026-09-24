import { describe, expect, it } from "vitest";
import { Api, ApiError } from "./api";

function recorder(status = 200, body: unknown = {}) {
  const calls: { input: string; init: RequestInit }[] = [];
  const api = new Api(async (input, init) => {
    calls.push({ input, init });
    const text = typeof body === "string" ? body : JSON.stringify(body);
    return new Response(status === 204 ? null : text, { status });
  });
  const headers = (i: number) => calls[i].init.headers as Record<string, string>;
  return { api, calls, headers };
}

describe("Api", () => {
  it("sends GETs with the session cookie and no Idempotency-Key", async () => {
    const { api, calls, headers } = recorder(200, { handle: "mel", role: "admin" });
    expect(await api.me()).toMatchObject({ handle: "mel" });
    expect(calls[0].input).toBe("/api/v1/me");
    expect(calls[0].init.credentials).toBe("same-origin");
    expect(headers(0)["Idempotency-Key"]).toBeUndefined();
  });

  it("sends changes as JSON with a new Idempotency-Key each time", async () => {
    const { api, calls, headers } = recorder(201, { handle: "mel" });
    await api.login("fs_abc");
    await api.login("fs_abc");
    expect(calls[0].init.method).toBe("POST");
    expect(calls[0].init.body).toBe('{"token":"fs_abc"}');
    expect(headers(0)["Content-Type"]).toBe("application/json");
    expect(headers(0)["Idempotency-Key"]).toMatch(/^[0-9a-f-]{36}$/);
    expect(headers(1)["Idempotency-Key"]).not.toBe(headers(0)["Idempotency-Key"]);
  });

  it("returns nothing for 204", async () => {
    const { api } = recorder(204);
    await expect(api.logout()).resolves.toBeUndefined();
  });

  it("turns API errors into ApiError with the server's message", async () => {
    const { api } = recorder(401, { error: "invalid, expired, or revoked token" });
    const err = await api.login("fs_bad").catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect(err).toMatchObject({ status: 401, message: "invalid, expired, or revoked token" });
  });

  it("handles non-JSON errors", async () => {
    const { api } = recorder(502, "<html>Bad Gateway</html>");
    await expect(api.me()).rejects.toMatchObject({ status: 502, message: "Request failed (HTTP 502)" });
  });
});
