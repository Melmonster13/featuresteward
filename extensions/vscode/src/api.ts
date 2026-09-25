// A read-only client for the FeatureSteward API. It has no VS Code
// dependencies, so it's tested on its own.

export type Role = "viewer" | "editor" | "approver" | "admin";

export interface User {
  handle: string;
  name: string;
  role: Role;
}

export interface Rule {
  attribute: "user_id" | "group";
  values: string[];
  serve: boolean;
}

export interface EnvConfig {
  enabled: boolean;
  rollout_percentage: number;
  rules: Rule[];
}

export interface Environment {
  key: string;
  name: string;
  protected: boolean;
}

export type StaleReason = "unused" | "always_on" | "always_off" | "settled_mixed";

export interface Flag {
  key: string;
  name: string;
  description: string;
  steward: string | null;
  environments: Record<string, EnvConfig>;
  permanent_reason: string | null;
  stale: { reason: StaleReason; since: string; suggestion: string } | null;
}

// checkURL returns why raw can't be used as the server URL, or "" if it
// can. The token goes with every request, so plain http is only allowed
// to this machine.
export function checkURL(raw: string): string {
  let u: URL;
  try {
    u = new URL(raw);
  } catch {
    return "Enter a URL like https://flags.example.com";
  }
  if (u.protocol !== "https:" && u.protocol !== "http:") return "The URL must start with https://";
  if (u.username || u.password) return "The URL must not contain a user name or password";
  if (u.protocol === "http:" && !["localhost", "127.0.0.1", "[::1]"].includes(u.hostname)) {
    return "Use https; plain http is only allowed to localhost";
  }
  return "";
}

// checkToken returns why value can't be an API token, or "" if it can.
export function checkToken(value: string): string {
  const t = value.trim();
  if (t.startsWith("fs_sdk_")) return "That's an SDK key; use an API token (fs_…) from the dashboard's Your tokens page";
  if (!/^fs_[A-Za-z0-9_-]+$/.test(t)) return "API tokens start with fs_";
  return "";
}

export class APIError extends Error {
  constructor(
    readonly status: number,
    message: string,
  ) {
    super(message);
  }
}

export class Client {
  private readonly base: string;

  constructor(
    url: string,
    private readonly token: string,
    private readonly fetchFn: typeof fetch = fetch,
  ) {
    this.base = url.replace(/\/+$/, "");
  }

  me(): Promise<User> {
    return this.get("/api/v1/me");
  }

  async flags(): Promise<Flag[]> {
    const body = await this.get<{ flags: Flag[] | null }>("/api/v1/flags");
    return body.flags ?? [];
  }

  async environments(): Promise<Environment[]> {
    const body = await this.get<{ environments: Environment[] | null }>("/api/v1/environments");
    return body.environments ?? [];
  }

  private async get<T>(path: string): Promise<T> {
    let res: Response;
    try {
      res = await this.fetchFn(this.base + path, {
        headers: { Authorization: `Bearer ${this.token}`, Accept: "application/json" },
        // Never follow a redirect with the token attached.
        redirect: "error",
        signal: AbortSignal.timeout(10_000),
      });
    } catch {
      throw new APIError(0, `Couldn't reach ${this.base}`);
    }
    if (!res.ok) throw new APIError(res.status, await errorMessage(res));
    return (await res.json()) as T;
  }
}

async function errorMessage(res: Response): Promise<string> {
  if (res.status === 401) return "The server rejected the token; sign in again";
  if (res.status === 429) return "Rate limited by the server; try again in a minute";
  try {
    const body = (await res.json()) as { error?: unknown };
    if (typeof body.error === "string" && body.error) return body.error.slice(0, 200);
  } catch {
    // Not JSON; fall through.
  }
  return `The server returned ${res.status}`;
}
