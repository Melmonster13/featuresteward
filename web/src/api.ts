// A typed client for the FeatureSteward API. The browser sends the
// session cookie; this code never sees it.

export type Role = "viewer" | "editor" | "approver" | "admin";

export interface User {
  handle: string;
  name: string;
  role: Role;
  created_at: string;
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

export interface Flag {
  key: string;
  name: string;
  description: string;
  steward: string | null;
  created_at: string;
  updated_at: string;
  archived_at?: string;
  environments: Record<string, EnvConfig>;
}

export interface Environment {
  key: string;
  name: string;
  protected: boolean;
}

export interface AuditEvent {
  id: number;
  occurred_at: string;
  actor: string;
  action: string;
  environment?: string;
  before: unknown;
  after: unknown;
}

export interface NewFlag {
  key: string;
  name: string;
  description: string;
  steward: string;
}

export class ApiError extends Error {
  constructor(
    readonly status: number,
    message: string,
  ) {
    super(message);
  }
}

type Fetch = (input: string, init: RequestInit) => Promise<Response>;

export class Api {
  constructor(private readonly fetchFn: Fetch = (input, init) => fetch(input, init)) {}

  me(): Promise<User> {
    return this.request("GET", "/api/v1/me");
  }

  login(token: string): Promise<User> {
    return this.request("POST", "/api/v1/session", { token });
  }

  logout(): Promise<void> {
    return this.request("DELETE", "/api/v1/session");
  }

  async environments(): Promise<Environment[]> {
    return (await this.request<{ environments: Environment[] }>("GET", "/api/v1/environments")).environments;
  }

  // flags lists active flags; steward filters by handle, or "none" for
  // flags with no active steward.
  async flags(steward = ""): Promise<Flag[]> {
    const query = steward ? `?steward=${encodeURIComponent(steward)}` : "";
    return (await this.request<{ flags: Flag[] }>("GET", `/api/v1/flags${query}`)).flags;
  }

  createFlag(flag: NewFlag): Promise<Flag> {
    return this.request("POST", "/api/v1/flags", flag);
  }

  // flag returns one flag, including an archived one.
  flag(key: string): Promise<Flag> {
    return this.request("GET", flagPath(key));
  }

  async audit(key: string): Promise<AuditEvent[]> {
    return (await this.request<{ events: AuditEvent[] }>("GET", `${flagPath(key)}/audit`)).events;
  }

  // setEnvironment replaces the flag's whole config in one environment.
  setEnvironment(key: string, env: string, cfg: EnvConfig): Promise<Flag> {
    return this.request("PUT", `${flagPath(key)}/environments/${encodeURIComponent(env)}`, cfg);
  }

  setSteward(key: string, steward: string): Promise<Flag> {
    return this.request("PUT", `${flagPath(key)}/steward`, { steward });
  }

  archiveFlag(key: string): Promise<void> {
    return this.request("DELETE", flagPath(key));
  }

  // Every change carries a fresh Idempotency-Key, so a retried request
  // can't be applied twice.
  async request<T>(method: string, path: string, body?: unknown): Promise<T> {
    const headers: Record<string, string> = { Accept: "application/json" };
    if (body !== undefined) headers["Content-Type"] = "application/json";
    if (method !== "GET") headers["Idempotency-Key"] = crypto.randomUUID();
    const res = await this.fetchFn(path, {
      method,
      headers,
      body: body === undefined ? undefined : JSON.stringify(body),
      credentials: "same-origin",
    });
    if (!res.ok) throw new ApiError(res.status, await errorMessage(res));
    if (res.status === 204) return undefined as T;
    return (await res.json()) as T;
  }
}

function flagPath(key: string): string {
  return `/api/v1/flags/${encodeURIComponent(key)}`;
}

async function errorMessage(res: Response): Promise<string> {
  try {
    const body: unknown = await res.json();
    if (body && typeof body === "object" && "error" in body && typeof body.error === "string") {
      return body.error;
    }
  } catch {
    // Not JSON, e.g. a proxy's error page.
  }
  return `Request failed (HTTP ${res.status})`;
}
