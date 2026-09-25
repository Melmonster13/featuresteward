import { ApiError, type ChangeRequest, type EnvConfig, type Environment, type Flag, type Rule } from "./api";
import { h } from "./dom";
import { ago, atLeast, envState, flagHash, isKillSwitch, lockReason, parseValues, sameConfig, staleLabel } from "./format";
import type { App } from "./main";
import { requestCard } from "./requests";
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
  const [envs, pending] = await Promise.all([app.api.environments(), app.api.requests({ status: "pending", flag: key })]);
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
    flag.stale && !archived
      ? h(
          "div",
          { class: "banner", role: "note" },
          h("strong", {}, `Stale: ${staleLabel(flag.stale.reason).toLowerCase()} since ${formatTime(flag.stale.since)}. `),
          flag.stale.suggestion,
        )
      : "",
    h("h2", {}, "Environments"),
    h(
      "div",
      { class: "env-grid" },
      ...envs.map((env) => envCard(app, flag, env, archived, changed, envName, pending.find((r) => r.environment === env.key))),
    ),
    canReassign ? stewardForm(app, flag, changed) : "",
    !archived ? permanentSection(app, flag, canReassign) : "",
    h("section", { "aria-labelledby": "history-title" }, h("h2", { id: "history-title" }, "History"), history),
    !archived && atLeast(app.user.role, "admin") ? archiveForm(app, flag) : "",
  ];
}

function envCard(
  app: App,
  flag: Flag,
  env: Environment,
  archived: boolean,
  changed: (f: Flag) => void,
  envName: (k: string) => string,
  pending: ChangeRequest | undefined,
): HTMLElement {
  const id = `env-${env.key}`;
  let saved: EnvConfig = structuredClone(flag.environments[env.key] ?? { enabled: false, rollout_percentage: 100, rules: [] });
  let rules: Rule[] = structuredClone(saved.rules ?? []);

  const why = lockReason(app.user.role, archived);
  const isAdmin = atLeast(app.user.role, "admin");

  // A pending request shows at the top of the card; while it's open, the
  // server won't take another one for this environment.
  const pendingBox = h("div");
  const setPending = (r: ChangeRequest | undefined) => {
    pending = r;
    pendingBox.replaceChildren(
      r
        ? requestCard(app, r, {
            steward: flag.steward,
            envName,
            showFlag: false,
            done: (message) => {
              app.flash(message);
              app.navigate(flagHash(flag.key));
            },
          })
        : "",
    );
  };
  setPending(pending);

  // What saving does here: a direct save, the kill switch, or a request.
  const mode = (): "save" | "kill" | "request" =>
    !env.protected ? "save" : isKillSwitch(saved, current()) ? "kill" : "request";
  const reasonId = `${id}-reason`;
  const reason = h("textarea", { id: reasonId, rows: 2, placeholder: "Why? Reviewers see this." });
  const reasonField = h("div", { class: "field" }, h("label", { for: reasonId }, "Reason"), reason);
  const emergency = h("button", { type: "button", class: "danger" }, "Apply now (emergency)");
  const modeHint = h("p", { class: "hint" });

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
    const m = mode();
    save.textContent = m === "save" ? "Save" : m === "kill" ? "Turn off now" : "Request change";
    save.disabled = !dirty || (m === "request" && Boolean(pending));
    discard.disabled = !dirty;
    reasonField.hidden = !dirty || m !== "request";
    emergency.hidden = !dirty || m !== "request" || !isAdmin;
    modeHint.textContent = !dirty
      ? ""
      : m === "kill"
        ? "Turning a flag off doesn't need approval."
        : m === "request" && pending
          ? "A request is already pending here. It has to be approved, rejected, or cancelled first."
          : m === "request"
            ? `Changes to ${env.name} need approval from the steward or an approver.${isAdmin ? " In an emergency, admins can apply them now with a reason." : ""}`
            : "";
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
    reason.value = "";
    load(saved);
  });

  // apply saves cfg directly; a reason makes it an emergency change.
  const apply = async (cfg: EnvConfig, why = "") => {
    const updated = await app.api.setEnvironment(flag.key, env.key, cfg, why);
    saved = structuredClone(updated.environments[env.key]);
    reason.value = "";
    load(saved);
    status.textContent = `Saved. ${env.name} is now ${envState(saved).label}.`;
    changed(updated);
  };
  const guard = async (fn: () => Promise<void>) => {
    error.textContent = "";
    const empty = current().rules.findIndex((r) => r.values.length === 0);
    if (empty >= 0) {
      error.textContent = `Rule ${empty + 1} needs at least one value.`;
      ruleList.querySelectorAll<HTMLElement>("li input")[empty]?.focus();
      return;
    }
    save.disabled = true;
    emergency.disabled = true;
    try {
      await fn();
    } catch (err) {
      if (err instanceof ApiError && err.status === 401) return app.fail(err);
      error.textContent = app.message(err);
    } finally {
      emergency.disabled = false;
      refresh();
    }
  };
  emergency.addEventListener("click", () => {
    if (!reason.value.trim()) {
      error.textContent = "An emergency change needs a reason.";
      reason.focus();
      return;
    }
    guard(() => apply(current(), reason.value.trim()));
  });

  const form = h(
    "form",
    {
      onsubmit: (event) => {
        event.preventDefault();
        if (mode() !== "request") return guard(() => apply(current()));
        guard(async () => {
          const r = await app.api.requestChange(flag.key, env.key, current(), reason.value.trim());
          reason.value = "";
          load(saved);
          setPending(r);
          status.textContent = `Requested. ${env.name} changes once the steward or an approver approves request #${r.id}.`;
          app.refreshReviews();
        });
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
      reasonField,
      modeHint,
      h("div", { class: "actions" }, save, emergency, discard),
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
    flag.activity?.[env.key]
      ? h(
          "p",
          { class: "hint" },
          `Changed ${ago(flag.activity[env.key].changed_at)} · last evaluated ${ago(flag.activity[env.key].evaluated_at)}`,
        )
      : "",
    pendingBox,
    form,
  );
}

// permanentSection shows whether a flag is meant to last. Admins and the
// flag's steward can change it; permanent flags are never reported stale.
function permanentSection(app: App, flag: Flag, canEdit: boolean): HTMLElement {
  const status = h("p", { class: "status", role: "status" });
  const error = h("p", { class: "error", role: "alert" });
  const save = async (reason: string) => {
    error.textContent = "";
    try {
      await app.api.setPermanent(flag.key, reason);
      app.flash(reason ? `${flag.key} is permanent. It won't be reported stale.` : `${flag.key} is no longer permanent.`);
      app.navigate(flagHash(flag.key));
    } catch (err) {
      if (err instanceof ApiError && err.status === 401) return app.fail(err);
      error.textContent = app.message(err);
    }
  };
  const title = h("h2", { id: "permanent-title" }, "Meant to last?");
  if (flag.permanent_reason) {
    return h(
      "section",
      { "aria-labelledby": "permanent-title" },
      title,
      h("p", {}, "Permanent: ", h("span", { class: "quote" }, flag.permanent_reason), ". It's never reported stale."),
      canEdit ? h("button", { type: "button", class: "secondary", onclick: () => save("") }, "Remove the permanent mark") : "",
      status,
      error,
    );
  }
  if (!canEdit) return h("span");
  const reason = h("input", { id: "permanent-reason", autocomplete: "off", maxlength: 500, placeholder: "e.g. payments kill switch" });
  return h(
    "section",
    { "aria-labelledby": "permanent-title" },
    title,
    h("p", { class: "hint" }, "Mark flags you mean to keep, like kill switches, so they're never reported stale."),
    h(
      "form",
      {
        class: "inline-form",
        onsubmit: (event) => {
          event.preventDefault();
          if (!reason.value.trim()) {
            error.textContent = "Say why it's meant to last.";
            reason.focus();
            return;
          }
          save(reason.value.trim());
        },
      },
      h("div", { class: "field" }, h("label", { for: "permanent-reason" }, "Reason"), reason),
      h("button", { type: "submit", class: "secondary" }, "Mark as permanent"),
    ),
    status,
    error,
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
