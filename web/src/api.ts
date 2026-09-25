// A typed client for the FeatureSteward API. The browser sends the
// session cookie; this code never sees it.

export type Role = "viewer" | "editor" | "approver" | "admin";

export interface User {
  handle: string;
  name: string;
  role: Role;
  created_at: string;
  disabled_at?: string;
}

// Token is an API token. The secret (token) is only in the response
// that creates it.
export interface Token {
  id: number;
  name: string;
  prefix: string;
  created_at: string;
  expires_at?: string;
  last_used_at?: string;
  revoked_at?: string;
  token?: string;
}

// SDKKey lets an app evaluate flags in one environment. The secret (key)
// is only in the response that creates it.
export interface SDKKey {
  id: number;
  environment: string;
  name: string;
  prefix: string;
  created_at: string;
  revoked_at?: string;
  key?: string;
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
  // Set when the flag is meant to last; it's never reported stale.
  permanent_reason: string | null;
  activity: Record<string, { changed_at: string; evaluated_at: string }>;
  // Set when the flag looks safe to remove.
  stale: Staleness | null;
}

export type StaleReason = "unused" | "always_on" | "always_off" | "settled_mixed";

export interface Staleness {
  reason: StaleReason;
  since: string;
  suggestion: string;
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

export type RequestStatus = "pending" | "approved" | "rejected" | "cancelled" | "expired";

// ChangeRequest proposes a config for a flag in a protected environment.
export interface ChangeRequest {
  id: number;
  flag: string;
  environment: string;
  requested_by: string;
  reason: string;
  base: EnvConfig;
  proposed: EnvConfig;
  status: RequestStatus;
  reviewed_by: string | null;
  review_comment: string;
  created_at: string;
  expires_at: string;
  resolved_at?: string;
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
  // flags with no active steward; staleOnly keeps only stale flags.
  async flags(steward = "", staleOnly = false): Promise<Flag[]> {
    const params = new URLSearchParams();
    if (steward) params.set("steward", steward);
    if (staleOnly) params.set("stale", "true");
    const query = params.toString() ? `?${params}` : "";
    return (await this.request<{ flags: Flag[] }>("GET", `/api/v1/flags${query}`)).flags;
  }

  // setPermanent marks a flag as meant to last, or clears it with "".
  setPermanent(key: string, reason: string): Promise<Flag> {
    return this.request("PUT", `${flagPath(key)}/permanent`, { reason });
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

  // setEnvironment replaces the flag's whole config in one environment. In
  // a protected environment, a reason makes it an admin's emergency change.
  setEnvironment(key: string, env: string, cfg: EnvConfig, reason = ""): Promise<Flag> {
    const body = reason ? { ...cfg, reason } : cfg;
    return this.request("PUT", `${flagPath(key)}/environments/${encodeURIComponent(env)}`, body);
  }

  // --- Change requests ---

  async requests(filter: { status?: RequestStatus; flag?: string } = {}): Promise<ChangeRequest[]> {
    const params = new URLSearchParams();
    if (filter.status) params.set("status", filter.status);
    if (filter.flag) params.set("flag", filter.flag);
    const query = params.toString() ? `?${params}` : "";
    return (await this.request<{ requests: ChangeRequest[] }>("GET", `/api/v1/requests${query}`)).requests;
  }

  requestChange(key: string, env: string, cfg: EnvConfig, reason: string): Promise<ChangeRequest> {
    return this.request("POST", `${flagPath(key)}/environments/${encodeURIComponent(env)}/requests`, { ...cfg, reason });
  }

  approve(id: number, comment: string): Promise<ChangeRequest> {
    return this.request("POST", `/api/v1/requests/${id}/approve`, { comment });
  }

  reject(id: number, comment: string): Promise<ChangeRequest> {
    return this.request("POST", `/api/v1/requests/${id}/reject`, { comment });
  }

  cancel(id: number): Promise<ChangeRequest> {
    return this.request("POST", `/api/v1/requests/${id}/cancel`);
  }

  setSteward(key: string, steward: string): Promise<Flag> {
    return this.request("PUT", `${flagPath(key)}/steward`, { steward });
  }

  archiveFlag(key: string): Promise<void> {
    return this.request("DELETE", flagPath(key));
  }

  // --- Your own tokens ---

  async myTokens(): Promise<Token[]> {
    return (await this.request<{ tokens: Token[] }>("GET", "/api/v1/me/tokens")).tokens;
  }

  createMyToken(name: string, expiresAt?: string): Promise<Token> {
    return this.request("POST", "/api/v1/me/tokens", { name, expires_at: expiresAt ?? null });
  }

  revokeMyToken(id: number): Promise<void> {
    return this.request("DELETE", `/api/v1/me/tokens/${id}`);
  }

  // --- Admin ---

  async users(): Promise<User[]> {
    return (await this.request<{ users: User[] }>("GET", "/api/v1/users")).users;
  }

  user(handle: string): Promise<User> {
    return this.request("GET", userPath(handle));
  }

  createUser(handle: string, name: string, role: Role): Promise<User> {
    return this.request("POST", "/api/v1/users", { handle, name, role });
  }

  setRole(handle: string, role: Role): Promise<User> {
    return this.request("PUT", `${userPath(handle)}/role`, { role });
  }

  disableUser(handle: string): Promise<void> {
    return this.request("DELETE", userPath(handle));
  }

  async userAudit(handle: string): Promise<AuditEvent[]> {
    return (await this.request<{ events: AuditEvent[] }>("GET", `${userPath(handle)}/audit`)).events;
  }

  async userTokens(handle: string): Promise<Token[]> {
    return (await this.request<{ tokens: Token[] }>("GET", `${userPath(handle)}/tokens`)).tokens;
  }

  createUserToken(handle: string, name: string, expiresAt?: string): Promise<Token> {
    return this.request("POST", `${userPath(handle)}/tokens`, { name, expires_at: expiresAt ?? null });
  }

  revokeUserToken(handle: string, id: number): Promise<void> {
    return this.request("DELETE", `${userPath(handle)}/tokens/${id}`);
  }

  async sdkKeys(): Promise<SDKKey[]> {
    return (await this.request<{ sdk_keys: SDKKey[] }>("GET", "/api/v1/sdk-keys")).sdk_keys;
  }

  createSDKKey(environment: string, name: string): Promise<SDKKey> {
    return this.request("POST", "/api/v1/sdk-keys", { environment, name });
  }

  revokeSDKKey(id: number): Promise<void> {
    return this.request("DELETE", `/api/v1/sdk-keys/${id}`);
  }

  createEnvironment(env: Environment): Promise<Environment> {
    return this.request("POST", "/api/v1/environments", env);
  }

  updateEnvironment(key: string, name: string, isProtected: boolean): Promise<Environment> {
    return this.request("PUT", `/api/v1/environments/${encodeURIComponent(key)}`, { name, protected: isProtected });
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

function userPath(handle: string): string {
  return `/api/v1/users/${encodeURIComponent(handle)}`;
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
