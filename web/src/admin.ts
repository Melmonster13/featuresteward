import { ApiError, type Environment, type Role, type SDKKey, type Token, type User } from "./api";
import { h } from "./dom";
import { atLeast, expiryDate, tokenStatus, userHash, validHandle } from "./format";
import type { App } from "./main";
import { confirmButton, field, formatTime, historyList, secretBox, select } from "./ui";

const roles: [Role, string][] = [
  ["viewer", "Viewer"],
  ["editor", "Editor"],
  ["approver", "Approver"],
  ["admin", "Admin"],
];

const expiries: [string, string][] = [
  ["90", "90 days"],
  ["30", "30 days"],
  ["365", "1 year"],
  ["0", "Never"],
];

// --- Your tokens ---

export async function tokensPage(app: App): Promise<Node[]> {
  const list = h("div");
  const secret = h("div");
  const refresh = async () => {
    const tokens = await app.api.myTokens();
    list.replaceChildren(
      tokenTable(tokens, async (t) => {
        await app.api.revokeMyToken(t.id);
        secret.replaceChildren();
        await refresh();
      }),
    );
  };
  await refresh();
  return [
    h("h1", { tabindex: "-1" }, "Your API tokens"),
    h(
      "p",
      { class: "lead" },
      "Tokens sign in to the dashboard and authenticate the stew CLI and scripts. They have your role. Revoking the token you signed in with signs you out.",
    ),
    h("h2", {}, "New token"),
    tokenForm("my-token", async (name, expires) => {
      const t = await app.api.createMyToken(name, expires);
      secret.replaceChildren(secretBox(t.token, "token"));
      await refresh();
    }, app),
    secret,
    h("h2", {}, "Tokens"),
    list,
  ];
}

function tokenForm(id: string, create: (name: string, expiresAt?: string) => Promise<void>, app: App): HTMLElement {
  const name = h("input", { id: `${id}-name`, required: true, autocomplete: "off", placeholder: "e.g. laptop, CI" });
  const expiry = select(`${id}-expiry`, expiries, "90");
  const error = h("p", { class: "error", role: "alert" });
  const submit = h("button", { type: "submit" }, "Create token");
  return h(
    "form",
    {
      class: "inline-form",
      onsubmit: async (event) => {
        event.preventDefault();
        error.textContent = "";
        if (!name.value.trim()) {
          error.textContent = "Give the token a name, so you can recognize it later.";
          name.focus();
          return;
        }
        submit.disabled = true;
        try {
          await create(name.value.trim(), expiryDate(Number(expiry.value)));
          name.value = "";
        } catch (err) {
          if (err instanceof ApiError && err.status === 401) return app.fail(err);
          error.textContent = app.message(err);
        } finally {
          submit.disabled = false;
        }
      },
    },
    field(`${id}-name`, "Name", name),
    field(`${id}-expiry`, "Expires in", expiry),
    submit,
    error,
  );
}

function tokenTable(tokens: Token[], revoke: (t: Token) => Promise<void>): HTMLElement {
  if (tokens.length === 0) return h("p", { class: "muted" }, "No tokens yet.");
  return table(
    ["Name", "Prefix", "Created", "Last used", "Expires", "Status", ""],
    [...tokens].reverse().map((t) => {
      const status = tokenStatus(t);
      return [
        t.name,
        h("code", {}, `${t.prefix}…`),
        formatTime(t.created_at),
        formatTime(t.last_used_at),
        t.expires_at ? formatTime(t.expires_at) : "Never",
        h("span", { class: `state ${status === "active" ? "on" : "off"}` }, capitalize(status)),
        status === "active" ? confirmButton("Revoke", "Confirm revoke", () => revoke(t), `Revoke token ${t.name}`) : "",
      ];
    }),
  );
}

// --- Users ---

export async function usersPage(app: App): Promise<Node[]> {
  if (!atLeast(app.user.role, "admin")) return needsAdmin("Users");
  const list = h("div");
  const secret = h("div");
  const refresh = async () => {
    const users = await app.api.users();
    list.replaceChildren(
      table(
        ["Handle", "Name", "Role", "Status", "Created"],
        users.map((u) => [
          h("a", { href: userHash(u.handle) }, h("code", {}, u.handle)),
          u.name || h("span", { class: "muted" }, "—"),
          capitalize(u.role),
          u.disabled_at ? h("span", { class: "state off" }, "Disabled") : h("span", { class: "state on" }, "Active"),
          formatTime(u.created_at),
        ]),
      ),
    );
  };

  const handle = h("input", { id: "user-handle", required: true, autocomplete: "off", spellcheck: "false", "aria-describedby": "user-handle-hint" });
  const name = h("input", { id: "user-name", autocomplete: "off" });
  const role = select("user-role", roles, "editor");
  const withToken = h("input", { id: "user-token", type: "checkbox", checked: true });
  const error = h("p", { class: "error", role: "alert" });
  const submit = h("button", { type: "submit" }, "Add user");
  const form = h(
    "form",
    {
      class: "card wide",
      onsubmit: async (event) => {
        event.preventDefault();
        error.textContent = "";
        secret.replaceChildren();
        const h_ = handle.value.trim();
        if (!validHandle(h_)) {
          error.textContent = "Handle must be 1–64 lowercase letters, digits, '.', '_' or '-'.";
          handle.focus();
          return;
        }
        submit.disabled = true;
        try {
          await app.api.createUser(h_, name.value.trim(), role.value as Role);
          if (withToken.checked) {
            const t = await app.api.createUserToken(h_, "onboarding", expiryDate(30));
            secret.replaceChildren(
              secretBox(t.token, "token", `Send it to @${h_} privately, for example in a direct message. It expires in 30 days.`),
            );
          }
          handle.value = "";
          name.value = "";
          await refresh();
        } catch (err) {
          if (err instanceof ApiError && err.status === 401) return app.fail(err);
          error.textContent = err instanceof ApiError && err.status === 409 ? `@${h_} already exists.` : app.message(err);
        } finally {
          submit.disabled = false;
        }
      },
    },
    field("user-handle", "Handle", handle),
    h("p", { id: "user-handle-hint", class: "hint" }, "Lowercase, e.g. sam or sam.lee. It can't be changed later."),
    field("user-name", "Name (optional)", name),
    field("user-role", "Role", role),
    h("div", { class: "switch-row" }, withToken, h("label", { for: "user-token" }, "Create a sign-in token for them")),
    error,
    h("div", { class: "actions" }, submit),
  );

  await refresh();
  return [h("h1", { tabindex: "-1" }, "Users"), h("h2", {}, "Add a user"), form, secret, h("h2", {}, "Everyone"), list];
}

export async function userPage(app: App, handle: string): Promise<(Node | string)[]> {
  if (!atLeast(app.user.role, "admin")) return needsAdmin("Users");
  let user: User;
  try {
    user = await app.api.user(handle);
  } catch (err) {
    if (err instanceof ApiError && err.status === 404) {
      return [h("h1", { tabindex: "-1" }, "User not found"), h("p", {}, h("a", { href: "#/admin/users" }, "Back to users"))];
    }
    throw err;
  }
  const self = user.handle === app.user.handle;
  const disabled = Boolean(user.disabled_at);

  const tokens = h("div");
  const history = h("div");
  const secret = h("div");
  const refresh = async () => {
    const [ts, events] = await Promise.all([app.api.userTokens(handle), app.api.userAudit(handle)]);
    tokens.replaceChildren(
      tokenTable(ts, async (t) => {
        await app.api.revokeUserToken(handle, t.id);
        secret.replaceChildren();
        await refresh();
      }),
    );
    history.replaceChildren(historyList(events));
  };

  const roleSelect = select("role-select", roles, user.role);
  const roleStatus = h("p", { class: "status", role: "status" });
  const roleError = h("p", { class: "error", role: "alert" });
  const roleForm = h(
    "form",
    {
      class: "inline-form",
      onsubmit: async (event) => {
        event.preventDefault();
        roleError.textContent = "";
        try {
          user = await app.api.setRole(handle, roleSelect.value as Role);
          roleStatus.textContent = `@${handle} is now ${user.role === "admin" ? "an" : "a"} ${user.role}.`;
          await refresh();
        } catch (err) {
          if (err instanceof ApiError && err.status === 401) return app.fail(err);
          roleError.textContent = app.message(err);
        }
      },
    },
    field("role-select", "Role", roleSelect),
    h("button", { type: "submit" }, "Change role"),
  );

  const disableError = h("p", { class: "error", role: "alert" });
  const disable = confirmButton("Disable user", "Confirm disable", async () => {
    try {
      await app.api.disableUser(handle);
      app.flash(`Disabled @${handle} and revoked their tokens.`);
      app.navigate("#/admin/users");
    } catch (err) {
      if (err instanceof ApiError && err.status === 401) return app.fail(err);
      disableError.textContent = app.message(err);
    }
  });

  await refresh();
  const editable = !self && !disabled;
  return [
    h("p", { class: "crumbs" }, h("a", { href: "#/admin/users" }, "Users"), " / ", h("code", {}, user.handle)),
    h("h1", { tabindex: "-1" }, user.name || `@${user.handle}`),
    h("p", { class: "meta" }, `@${user.handle} · ${capitalize(user.role)} · added ${formatTime(user.created_at)}`),
    disabled ? h("p", { class: "banner", role: "note" }, `Disabled ${formatTime(user.disabled_at)}. Their tokens and sessions no longer work.`) : "",
    self
      ? h(
          "p",
          { class: "hint" },
          "This is you. Manage your tokens on ",
          h("a", { href: "#/tokens" }, "Your tokens"),
          ". Another admin has to change your role or disable you.",
        )
      : "",
    editable ? h("section", {}, h("h2", {}, "Role"), roleForm, roleStatus, roleError) : "",
    h("h2", {}, "Tokens"),
    editable
      ? tokenForm("user-token", async (name, expires) => {
          const t = await app.api.createUserToken(handle, name, expires);
          secret.replaceChildren(secretBox(t.token, "token", `Send it to @${handle} privately.`));
          await refresh();
        }, app)
      : "",
    secret,
    tokens,
    h("h2", {}, "History"),
    history,
    editable
      ? h(
          "section",
          { class: "danger-zone" },
          h("h2", {}, "Disable"),
          h("p", {}, "Disabling revokes every token and ends every session. The handle stays reserved, and their history stays in the audit log."),
          disable,
          disableError,
        )
      : "",
  ];
}

// --- SDK keys ---

export async function sdkKeysPage(app: App): Promise<Node[]> {
  if (!atLeast(app.user.role, "admin")) return needsAdmin("SDK keys");
  const envs = await app.api.environments();
  const envName = (k: string) => envs.find((e) => e.key === k)?.name ?? k;
  const list = h("div");
  const secret = h("div");
  const refresh = async () => {
    const keys = await app.api.sdkKeys();
    list.replaceChildren(
      sdkKeyTable(keys, envName, async (k) => {
        await app.api.revokeSDKKey(k.id);
        secret.replaceChildren();
        await refresh();
      }),
    );
  };

  const env = select("key-env", envs.map((e): [string, string] => [e.key, e.name]), envs[0]?.key ?? "");
  const name = h("input", { id: "key-name", required: true, autocomplete: "off", placeholder: "e.g. checkout-service" });
  const error = h("p", { class: "error", role: "alert" });
  const submit = h("button", { type: "submit" }, "Create key");
  const form = h(
    "form",
    {
      class: "inline-form",
      onsubmit: async (event) => {
        event.preventDefault();
        error.textContent = "";
        if (!name.value.trim()) {
          error.textContent = "Name the key after the app that will use it.";
          name.focus();
          return;
        }
        submit.disabled = true;
        try {
          const k = await app.api.createSDKKey(env.value, name.value.trim());
          secret.replaceChildren(secretBox(k.key, "SDK key", "Store it in your app's secret manager, not in code."));
          name.value = "";
          await refresh();
        } catch (err) {
          if (err instanceof ApiError && err.status === 401) return app.fail(err);
          error.textContent = app.message(err);
        } finally {
          submit.disabled = false;
        }
      },
    },
    field("key-env", "Environment", env),
    field("key-name", "App name", name),
    submit,
    error,
  );

  await refresh();
  return [
    h("h1", { tabindex: "-1" }, "SDK keys"),
    h("p", { class: "lead" }, "Apps use an SDK key to evaluate flags in one environment. A key can't do anything else."),
    h("h2", {}, "New key"),
    form,
    secret,
    h("h2", {}, "Keys"),
    list,
  ];
}

function sdkKeyTable(keys: SDKKey[], envName: (k: string) => string, revoke: (k: SDKKey) => Promise<void>): HTMLElement {
  if (keys.length === 0) return h("p", { class: "muted" }, "No SDK keys yet.");
  return table(
    ["Environment", "App", "Prefix", "Created", "Status", ""],
    keys.map((k) => {
      const status = tokenStatus(k);
      return [
        envName(k.environment),
        k.name,
        h("code", {}, `${k.prefix}…`),
        formatTime(k.created_at),
        h("span", { class: `state ${status === "active" ? "on" : "off"}` }, capitalize(status)),
        status === "active" ? confirmButton("Revoke", "Confirm revoke", () => revoke(k), `Revoke SDK key ${k.name}`) : "",
      ];
    }),
  );
}

// --- Environments ---

export async function environmentsPage(app: App): Promise<Node[]> {
  if (!atLeast(app.user.role, "admin")) return needsAdmin("Environments");
  const list = h("div");
  const refresh = async () => {
    const envs = await app.api.environments();
    list.replaceChildren(h("div", { class: "env-list" }, ...envs.map((e) => envRow(app, e))));
  };

  const key = h("input", { id: "env-key", required: true, autocomplete: "off", spellcheck: "false", maxlength: 32, placeholder: "e.g. qa" });
  const name = h("input", { id: "env-name", required: true, autocomplete: "off", placeholder: "e.g. QA" });
  const prot = h("input", { id: "env-protected", type: "checkbox" });
  const error = h("p", { class: "error", role: "alert" });
  const status = h("p", { class: "status", role: "status" });
  const submit = h("button", { type: "submit" }, "Add environment");
  const form = h(
    "form",
    {
      class: "inline-form",
      onsubmit: async (event) => {
        event.preventDefault();
        error.textContent = "";
        status.textContent = "";
        submit.disabled = true;
        try {
          const env = await app.api.createEnvironment({ key: key.value.trim(), name: name.value.trim(), protected: prot.checked });
          status.textContent = `Added ${env.name}. Every flag starts off there.`;
          key.value = "";
          name.value = "";
          prot.checked = false;
          await refresh();
        } catch (err) {
          if (err instanceof ApiError && err.status === 401) return app.fail(err);
          error.textContent =
            err instanceof ApiError && err.status === 409 ? `An environment called ${key.value.trim()} already exists.` : app.message(err);
        } finally {
          submit.disabled = false;
        }
      },
    },
    field("env-key", "Key", key),
    field("env-name", "Name", name),
    h("div", { class: "switch-row" }, prot, h("label", { for: "env-protected" }, "Protected")),
    submit,
  );

  await refresh();
  return [
    h("h1", { tabindex: "-1" }, "Environments"),
    h("p", { class: "lead" }, "Only admins can change flags in a protected environment, until approvals are available."),
    list,
    h("h2", {}, "Add an environment"),
    form,
    status,
    error,
  ];
}

function envRow(app: App, env: Environment): HTMLElement {
  const id = `env-${env.key}`;
  const name = h("input", { id: `${id}-name`, required: true, autocomplete: "off" });
  name.value = env.name;
  const prot = h("input", { id: `${id}-protected`, type: "checkbox", checked: env.protected });
  const status = h("p", { class: "status", role: "status" });
  const error = h("p", { class: "error", role: "alert" });
  return h(
    "form",
    {
      class: "env-row",
      "aria-label": `${env.name} settings`,
      onsubmit: async (event) => {
        event.preventDefault();
        error.textContent = "";
        status.textContent = "";
        try {
          const updated = await app.api.updateEnvironment(env.key, name.value.trim(), prot.checked);
          status.textContent = `Saved. ${updated.name} is ${updated.protected ? "protected" : "not protected"}.`;
        } catch (err) {
          if (err instanceof ApiError && err.status === 401) return app.fail(err);
          error.textContent = app.message(err);
        }
      },
    },
    h("code", {}, env.key),
    field(`${id}-name`, "Name", name),
    h("div", { class: "switch-row" }, prot, h("label", { for: `${id}-protected` }, "Protected")),
    h("button", { type: "submit", class: "secondary" }, "Save"),
    status,
    error,
  );
}

// --- Shared ---

function table(headings: string[], rows: (Node | string)[][]): HTMLElement {
  return h(
    "div",
    { class: "table-scroll" },
    h(
      "table",
      {},
      h("thead", {}, h("tr", {}, ...headings.map((t) => h("th", { scope: "col" }, t ? t : h("span", { class: "sr-only" }, "Actions"))))),
      h("tbody", {}, ...rows.map((cells) => h("tr", {}, ...cells.map((c) => h("td", {}, c))))),
    ),
  );
}

function needsAdmin(title: string): Node[] {
  return [h("h1", { tabindex: "-1" }, title), h("p", {}, "This page needs the admin role.")];
}

function capitalize(s: string): string {
  return s.charAt(0).toUpperCase() + s.slice(1);
}
