import type { AuditEvent } from "./api";
import { h } from "./dom";
import { describeEvent } from "./format";

export function field(id: string, label: string, control: HTMLElement): HTMLElement {
  return h("div", { class: "field" }, h("label", { for: id }, label), control);
}

export function select(id: string, options: [string, string][], value: string): HTMLSelectElement {
  const el = h("select", { id }, ...options.map(([v, text]) => h("option", { value: v }, text)));
  el.value = value;
  return el;
}

const timeFormat = new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeStyle: "short" });

export function formatTime(iso: string | undefined): string {
  return iso ? timeFormat.format(new Date(iso)) : "—";
}

// historyList shows audit events as sentences, newest first.
export function historyList(events: AuditEvent[], envName: (key: string) => string = (k) => k): HTMLElement {
  if (events.length === 0) return h("p", { class: "muted" }, "No history yet.");
  return h(
    "ol",
    { class: "history" },
    ...[...events].reverse().map((e) =>
      h(
        "li",
        {},
        h("time", { datetime: e.occurred_at }, formatTime(e.occurred_at)),
        " ",
        h("strong", {}, `@${e.actor}`),
        ` ${describeEvent(e, envName)}`,
      ),
    ),
  );
}

// secretBox shows a new token or SDK key once. The secret lives only in
// this element; it isn't stored anywhere in the page.
export function secretBox(secret: string | undefined, what: string, note = ""): HTMLElement {
  if (!secret) {
    return h(
      "p",
      { class: "banner", role: "alert" },
      `The ${what} was created, but its secret can't be shown again. Revoke it and create a new one.`,
    );
  }
  const input = h("input", { id: "new-secret", readonly: true, spellcheck: "false", autocomplete: "off" });
  input.value = secret;
  const status = h("span", { class: "status", role: "status" });
  const copy = h(
    "button",
    {
      type: "button",
      class: "secondary",
      onclick: async () => {
        try {
          await navigator.clipboard.writeText(secret);
          status.textContent = "Copied.";
        } catch {
          input.select();
          status.textContent = "Selected. Press Ctrl+C or ⌘C to copy.";
        }
      },
    },
    "Copy",
  );
  return h(
    "div",
    { class: "secret", role: "region", "aria-labelledby": "secret-title" },
    h(
      "p",
      { id: "secret-title" },
      h("strong", {}, `Copy this ${what} now.`),
      ` It won't be shown again; FeatureSteward keeps only a hash of it. ${note}`,
    ),
    h("div", { class: "secret-row" }, h("label", { for: "new-secret", class: "sr-only" }, `New ${what}`), input, copy),
    status,
  );
}

// confirmButton needs a second click within five seconds to act.
export function confirmButton(label: string, confirmLabel: string, action: () => Promise<void>, accessibleName = label): HTMLButtonElement {
  let timer: ReturnType<typeof setTimeout> | undefined;
  const button = h("button", { type: "button", class: "secondary small", "aria-label": accessibleName }, label);
  const reset = () => {
    clearTimeout(timer);
    timer = undefined;
    button.textContent = label;
    button.setAttribute("aria-label", accessibleName);
    button.classList.remove("danger");
  };
  button.addEventListener("click", async () => {
    if (!timer) {
      button.textContent = confirmLabel;
      button.setAttribute("aria-label", `${confirmLabel}: ${accessibleName}`);
      button.classList.add("danger");
      timer = setTimeout(reset, 5000);
      return;
    }
    reset();
    button.disabled = true;
    try {
      await action();
    } finally {
      button.disabled = false;
    }
  });
  return button;
}
