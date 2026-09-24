import { ApiError, type EnvConfig, type Environment, type Flag, type Rule } from "./api";
import { h } from "./dom";
import { atLeast, envState, lockReason, parseValues, sameConfig } from "./format";
import type { App } from "./main";
import { formatTime, historyList } from "./ui";

export async function flagPage(app: App, key: string): Promise<(Node | string)[]> {
  let flag: Flag;
  try {
    flag = await app.api.flag(key);
  } catch (err) {
    if (err instanceof ApiError && err.status === 404) {
      return [h("h1", { tabindex: "-1" }, "Flag not found"), h("p", {}, `There's no flag called ${key}. `, h("a", { href: "#/flags" }, "Back to flags"))];
    }
    throw err;
  }
  const envs = await app.api.environments();
  const envName = (k: string) => envs.find((e) => e.key === k)?.name ?? k;
  const archived = Boolean(flag.archived_at);

  const history = h("div");
  const refreshHistory = async () => {
    try {
      history.replaceChildren(historyList(await app.api.audit(key), envName));
    } catch (err) {
      app.fail(err);
    }
  };
  // Other parts of the page call this after a change.
  const changed = (updated: Flag) => {
    flag = updated;
    stewardText.textContent = flag.steward ? `@${flag.steward}` : "None";
    refreshHistory();
  };

  const stewardText = h("span", {}, flag.steward ? `@${flag.steward}` : "None");
  const canReassign = !archived && (atLeast(app.user.role, "admin") || flag.steward === app.user.handle);

  await refreshHistory();
  return [
    h("p", { class: "crumbs" }, h("a", { href: "#/flags" }, "Flags"), " / ", h("code", {}, flag.key)),
    h("h1", { tabindex: "-1" }, flag.name),
    flag.description ? h("p", { class: "lead" }, flag.description) : "",
    archived
      ? h("p", { class: "banner", role: "note" }, `Archived ${formatTime(flag.archived_at!)}. Evaluating it returns not found, and it can't be changed.`)
      : "",
    h("p", { class: "meta" }, h("span", {}, "Key "), h("code", {}, flag.key), h("span", {}, " · Steward "), stewardText),
    h("h2", {}, "Environments"),
    h("div", { class: "env-grid" }, ...envs.map((env) => envCard(app, flag, env, archived, changed))),
    canReassign ? stewardForm(app, flag, changed) : "",
    h("section", { "aria-labelledby": "history-title" }, h("h2", { id: "history-title" }, "History"), history),
    !archived && atLeast(app.user.role, "admin") ? archiveForm(app, flag) : "",
  ];
}

function envCard(app: App, flag: Flag, env: Environment, archived: boolean, changed: (f: Flag) => void): HTMLElement {
  const id = `env-${env.key}`;
  let saved: EnvConfig = structuredClone(flag.environments[env.key] ?? { enabled: false, rollout_percentage: 100, rules: [] });
  let rules: Rule[] = structuredClone(saved.rules ?? []);

  const why = lockReason(app.user.role, env, archived);

  const enabled = h("input", { id: `${id}-enabled`, type: "checkbox", role: "switch" });
  const rollout = h("input", { id: `${id}-rollout`, type: "range", min: 0, max: 100, step: 1 });
  const rolloutOut = h("output", { for: `${id}-rollout` });
  const ruleList = h("ol", { class: "rules" });
  const status = h("p", { class: "status", role: "status" });
  const error = h("p", { class: "error", role: "alert" });
  const save = h("button", { type: "submit" }, "Save");
  const discard = h("button", { type: "button", class: "secondary" }, "Discard");
  const addRule = h("button", { type: "button", class: "secondary small" }, "Add rule");
  const summary = h("span", { class: "state" });

  const current = (): EnvConfig => ({ enabled: enabled.checked, rollout_percentage: Number(rollout.value), rules });
  const refresh = () => {
    rolloutOut.textContent = `${rollout.value}%`;
    const s = envState(saved);
    summary.className = `state ${s.kind}`;
    summary.textContent = s.label;
    const dirty = !sameConfig(current(), saved);
    save.disabled = !dirty;
    discard.disabled = !dirty;
    if (dirty) status.textContent = "Unsaved changes";
    else if (status.textContent === "Unsaved changes") status.textContent = "";
  };
  const load = (cfg: EnvConfig) => {
    enabled.checked = cfg.enabled;
    rollout.value = String(cfg.rollout_percentage);
    rules = structuredClone(cfg.rules ?? []);
    renderRules();
    refresh();
  };
  const renderRules = () => {
    ruleList.replaceChildren(
      ...rules.map((rule, i) => {
        const n = i + 1;
        const attribute = h(
          "select",
          { "aria-label": `Rule ${n}: match by` },
          h("option", { value: "user_id" }, "User ID"),
          h("option", { value: "group" }, "Group"),
        );
        attribute.value = rule.attribute;
        attribute.addEventListener("change", () => {
          rule.attribute = attribute.value as Rule["attribute"];
          refresh();
        });
        const values = h("input", {
          "aria-label": `Rule ${n}: values, separated by commas`,
          value: rule.values.join(", "),
          placeholder: "e.g. staff, beta",
          autocomplete: "off",
        });
        values.addEventListener("input", () => {
          rule.values = parseValues(values.value);
          refresh();
        });
        const serve = h(
          "select",
          { "aria-label": `Rule ${n}: serve` },
          h("option", { value: "on" }, "serve On"),
          h("option", { value: "off" }, "serve Off"),
        );
        serve.value = rule.serve ? "on" : "off";
        serve.addEventListener("change", () => {
          rule.serve = serve.value === "on";
          refresh();
        });
        const remove = h(
          "button",
          {
            type: "button",
            class: "secondary small",
            "aria-label": `Remove rule ${n}`,
            onclick: () => {
              rules.splice(i, 1);
              renderRules();
              refresh();
            },
          },
          "Remove",
        );
        return h("li", {}, attribute, values, serve, remove);
      }),
    );
  };

  enabled.addEventListener("change", refresh);
  rollout.addEventListener("input", refresh);
  addRule.addEventListener("click", () => {
    rules.push({ attribute: "group", values: [], serve: true });
    renderRules();
    refresh();
    ruleList.querySelector<HTMLElement>("li:last-child input")?.focus();
  });
  discard.addEventListener("click", () => {
    status.textContent = "";
    error.textContent = "";
    load(saved);
  });

  const form = h(
    "form",
    {
      onsubmit: async (event) => {
        event.preventDefault();
        error.textContent = "";
        const cfg = current();
        const empty = cfg.rules.findIndex((r) => r.values.length === 0);
        if (empty >= 0) {
          error.textContent = `Rule ${empty + 1} needs at least one value.`;
          ruleList.querySelectorAll<HTMLElement>("li input")[empty]?.focus();
          return;
        }
        save.disabled = true;
        try {
          const updated = await app.api.setEnvironment(flag.key, env.key, cfg);
          saved = structuredClone(updated.environments[env.key]);
          load(saved);
          status.textContent = `Saved. ${env.name} is now ${envState(saved).label}.`;
          changed(updated);
        } catch (err) {
          if (err instanceof ApiError && err.status === 401) return app.fail(err);
          error.textContent = app.message(err);
          refresh();
        }
      },
    },
    h(
      "fieldset",
      { disabled: Boolean(why) },
      h("legend", { class: "sr-only" }, `${env.name} settings`),
      h("div", { class: "switch-row" }, enabled, h("label", { for: `${id}-enabled` }, "Enabled")),
      h("div", { class: "field" }, h("label", { for: `${id}-rollout` }, "Rollout ", rolloutOut), rollout),
      h(
        "div",
        { class: "field" },
        h("span", { class: "label" }, "Targeting rules"),
        h("p", { class: "hint" }, "When enabled, the first matching rule decides. Everyone else gets the rollout."),
        ruleList,
        h("div", {}, addRule),
      ),
      h("div", { class: "actions" }, save, discard),
    ),
    status,
    error,
  );
  load(saved);

  return h(
    "section",
    { class: "env-card", "aria-labelledby": `${id}-title` },
    h(
      "h3",
      { id: `${id}-title` },
      env.name,
      ...(env.protected ? [" ", h("span", { class: "tag" }, "protected")] : []),
      " ",
      summary,
    ),
    why ? h("p", { class: "hint" }, why) : "",
    form,
  );
}

function stewardForm(app: App, flag: Flag, changed: (f: Flag) => void): HTMLElement {
  const input = h("input", {
    id: "steward-handle",
    autocomplete: "off",
    spellcheck: "false",
    placeholder: flag.steward ?? "handle",
    "aria-describedby": "steward-hint",
  });
  const status = h("p", { class: "status", role: "status" });
  const error = h("p", { class: "error", role: "alert" });
  const submit = h("button", { type: "submit" }, "Reassign");
  return h(
    "section",
    { "aria-labelledby": "steward-title" },
    h("h2", { id: "steward-title" }, "Steward"),
    h(
      "form",
      {
        class: "inline-form",
        onsubmit: async (event) => {
          event.preventDefault();
          error.textContent = "";
          status.textContent = "";
          const handle = input.value.trim().replace(/^@/, "");
          if (!handle) {
            error.textContent = "Enter the new steward's handle.";
            input.focus();
            return;
          }
          submit.disabled = true;
          try {
            const updated = await app.api.setSteward(flag.key, handle);
            input.value = "";
            input.placeholder = handle;
            status.textContent = `@${handle} is now the steward.`;
            changed(updated);
          } catch (err) {
            if (err instanceof ApiError && err.status === 401) return app.fail(err);
            error.textContent = app.message(err);
          } finally {
            submit.disabled = false;
          }
        },
      },
      h("div", { class: "field" }, h("label", { for: "steward-handle" }, "New steward"), input),
      submit,
    ),
    h("p", { id: "steward-hint", class: "hint" }, "Must be an active editor or above. Afterwards, only the new steward or an admin can reassign it."),
    status,
    error,
  );
}

function archiveForm(app: App, flag: Flag): HTMLElement {
  const input = h("input", { id: "archive-confirm", autocomplete: "off", spellcheck: "false" });
  const error = h("p", { class: "error", role: "alert" });
  const submit = h("button", { type: "submit", class: "danger", disabled: true }, "Archive flag");
  input.addEventListener("input", () => {
    submit.disabled = input.value.trim() !== flag.key;
  });
  return h(
    "section",
    { class: "danger-zone", "aria-labelledby": "archive-title" },
    h("h2", { id: "archive-title" }, "Archive"),
    h("p", {}, "Archiving removes the flag from lists, and evaluating it returns not found. It can't be undone."),
    h(
      "form",
      {
        class: "inline-form",
        onsubmit: async (event) => {
          event.preventDefault();
          if (input.value.trim() !== flag.key) return;
          submit.disabled = true;
          try {
            await app.api.archiveFlag(flag.key);
            app.flash(`Archived ${flag.key}.`);
            app.navigate("#/flags");
          } catch (err) {
            if (err instanceof ApiError && err.status === 401) return app.fail(err);
            error.textContent = app.message(err);
            submit.disabled = false;
          }
        },
      },
      h("div", { class: "field" }, h("label", { for: "archive-confirm" }, `Type ${flag.key} to confirm`), input),
      submit,
    ),
    error,
  );
}
