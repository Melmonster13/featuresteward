import { ApiError, type Environment, type Flag } from "./api";
import type { App } from "./main";
import { atLeast, envState, flagHash, flagsHash, matchesSearch, validKey } from "./format";
import { h } from "./dom";
import { field, select } from "./ui";

export async function flagListPage(app: App, params: URLSearchParams): Promise<Node[]> {
  const filters = { q: params.get("q") ?? "", env: params.get("env") ?? "", steward: params.get("steward") ?? "" };
  const stewardParam = () => (filters.steward === "mine" ? app.user.handle : filters.steward === "none" ? "none" : "");
  let [envs, flags] = await Promise.all([app.api.environments(), app.api.flags(stewardParam())]);

  const results = h("div", { class: "results" });
  const count = h("p", { class: "count", role: "status" });
  const remember = () => history.replaceState(null, "", flagsHash(filters));

  const render = () => {
    const shown = flags.filter((f) => matchesSearch(f, filters.q));
    const columns = filters.env ? envs.filter((e) => e.key === filters.env) : envs;
    count.textContent = `${shown.length} of ${flags.length} flag${flags.length === 1 ? "" : "s"}`;
    results.replaceChildren(shown.length ? flagTable(shown, columns) : emptyState(app, filters.steward, flags.length > 0));
  };

  const search = h("input", { id: "flag-search", type: "search", value: filters.q, placeholder: "Key, name, or steward" });
  search.addEventListener("input", () => {
    filters.q = search.value;
    remember();
    render();
  });

  const envSelect = select("flag-env", [["", "All environments"], ...envs.map((e): [string, string] => [e.key, e.name])], filters.env);
  envSelect.addEventListener("change", () => {
    filters.env = envSelect.value;
    remember();
    render();
  });

  const stewardSelect = select("flag-steward", [["", "Everyone"], ["mine", "My flags"], ["none", "No steward"]], filters.steward);
  stewardSelect.addEventListener("change", async () => {
    filters.steward = stewardSelect.value;
    remember();
    stewardSelect.disabled = true;
    try {
      flags = await app.api.flags(stewardParam());
      render();
    } catch (err) {
      app.fail(err);
    } finally {
      stewardSelect.disabled = false;
    }
  });

  render();
  const canCreate = atLeast(app.user.role, "editor");
  return [
    h(
      "div",
      { class: "page-head" },
      h("h1", { tabindex: "-1" }, "Flags"),
      canCreate ? h("a", { class: "button", href: "#/flags/new" }, "New flag") : "",
    ),
    h(
      "form",
      { class: "filters", role: "search", onsubmit: (e) => e.preventDefault() },
      field("flag-search", "Search", search),
      field("flag-env", "Environment", envSelect),
      field("flag-steward", "Steward", stewardSelect),
    ),
    count,
    results,
  ];
}

function flagTable(flags: Flag[], envs: Environment[]): HTMLElement {
  return h(
    "div",
    { class: "table-scroll" },
    h(
      "table",
      {},
      h(
        "thead",
        {},
        h(
          "tr",
          {},
          h("th", { scope: "col" }, "Flag"),
          ...envs.map((e) =>
            h("th", { scope: "col" }, e.name, ...(e.protected ? [" ", h("span", { class: "tag", title: "Changes need approval" }, "protected")] : [])),
          ),
          h("th", { scope: "col" }, "Steward"),
        ),
      ),
      h(
        "tbody",
        {},
        ...flags.map((f) =>
          h(
            "tr",
            {},
            h("th", { scope: "row" }, h("a", { href: flagHash(f.key) }, h("code", {}, f.key)), h("span", { class: "sub" }, f.name)),
            ...envs.map((e) => {
              const s = envState(f.environments[e.key]);
              return h("td", {}, h("span", { class: `state ${s.kind}` }, s.label));
            }),
            h("td", {}, f.steward ? `@${f.steward}` : h("span", { class: "muted" }, "None")),
          ),
        ),
      ),
    ),
  );
}

function emptyState(app: App, steward: string, searched: boolean): HTMLElement {
  if (searched) return h("p", { class: "empty" }, "No flags match your search.");
  if (steward === "mine") return h("p", { class: "empty" }, "You aren't the steward of any flags.");
  if (steward === "none") return h("p", { class: "empty" }, "Every flag has an active steward.");
  return h(
    "p",
    { class: "empty" },
    "No flags here yet. ",
    atLeast(app.user.role, "editor") ? h("a", { href: "#/flags/new" }, "Create a flag") : "",
  );
}

export function newFlagPage(app: App): Node[] {
  if (!atLeast(app.user.role, "editor")) {
    return [h("h1", { tabindex: "-1" }, "New flag"), h("p", {}, "Creating flags needs the editor role or higher.")];
  }
  const key = h("input", {
    id: "new-key", required: true, maxlength: 100, autocomplete: "off", spellcheck: "false",
    pattern: "[a-z0-9][a-z0-9\\-]*", "aria-describedby": "new-key-hint",
  });
  const name = h("input", { id: "new-name", required: true, autocomplete: "off" });
  const description = h("textarea", { id: "new-description", rows: 3 });
  const steward = h("input", {
    id: "new-steward", autocomplete: "off", spellcheck: "false", placeholder: app.user.handle,
    "aria-describedby": "new-steward-hint",
  });
  const error = h("p", { class: "error", role: "alert" });
  const submit = h("button", { type: "submit" }, "Create flag");

  const form = h(
    "form",
    {
      class: "card wide",
      novalidate: true,
      onsubmit: async (event) => {
        event.preventDefault();
        error.textContent = "";
        const values = {
          key: key.value.trim(),
          name: name.value.trim(),
          description: description.value.trim(),
          steward: steward.value.trim().replace(/^@/, ""),
        };
        const problem = !validKey(values.key)
          ? [key, "Key must be lowercase letters, digits, and dashes, starting with a letter or digit."]
          : !values.name
            ? [name, "Name is required."]
            : null;
        if (problem) {
          error.textContent = problem[1] as string;
          (problem[0] as HTMLElement).focus();
          return;
        }
        submit.disabled = true;
        try {
          const flag = await app.api.createFlag(values);
          app.flash(`Created ${flag.key}. It's off in every environment.`);
          app.navigate(flagHash(flag.key));
        } catch (err) {
          if (err instanceof ApiError && err.status === 401) return app.fail(err);
          error.textContent = err instanceof ApiError && err.status === 409 ? `A flag called ${values.key} already exists.` : app.message(err);
          submit.disabled = false;
        }
      },
    },
    field("new-key", "Key", key),
    h("p", { id: "new-key-hint", class: "hint" }, "Used in code, e.g. new-checkout. Lowercase letters, digits, and dashes. It can't be changed later."),
    field("new-name", "Name", name),
    field("new-description", "Description (optional)", description),
    field("new-steward", "Steward (optional)", steward),
    h("p", { id: "new-steward-hint", class: "hint" }, "Defaults to you. Must be an active editor or above."),
    error,
    h("div", { class: "actions" }, submit, h("a", { href: "#/flags" }, "Cancel")),
  );
  return [h("h1", { tabindex: "-1" }, "New flag"), form];
}
