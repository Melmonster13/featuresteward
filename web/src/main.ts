import "./style.css";
import { Api, ApiError, type User } from "./api";
import { h } from "./dom";

const api = new Api();
const root = document.getElementById("app")!;

function show(...nodes: Node[]): void {
  root.replaceChildren(...nodes);
}

function messageOf(err: unknown): string {
  if (err instanceof ApiError) return err.message;
  return "Can't reach the FeatureSteward API. Check your connection and try again.";
}

async function start(): Promise<void> {
  try {
    signedIn(await api.me());
  } catch (err) {
    if (err instanceof ApiError && err.status === 401) loginPage();
    else show(h("main", {}, h("p", { class: "error", role: "alert" }, messageOf(err))));
  }
}

function loginPage(notice = ""): void {
  const input = h("input", {
    id: "token",
    type: "password",
    autocomplete: "off",
    spellcheck: "false",
    required: true,
    "aria-describedby": "token-hint",
  });
  const error = h("p", { class: "error", role: "alert" });
  const button = h("button", { type: "submit" }, "Sign in");
  const form = h(
    "form",
    {
      class: "card",
      onsubmit: async (event) => {
        event.preventDefault();
        button.disabled = true;
        error.textContent = "";
        try {
          const user = await api.login(input.value.trim());
          input.value = "";
          signedIn(user);
        } catch (err) {
          error.textContent = messageOf(err);
          button.disabled = false;
          input.focus();
        }
      },
    },
    h("h1", {}, "Sign in to FeatureSteward"),
    h("p", { class: "notice", role: "status" }, notice),
    h("label", { for: "token" }, "API token"),
    input,
    h("p", { id: "token-hint", class: "hint" }, "Paste a token that starts with fs_. Ask an admin if you don't have one."),
    error,
    button,
  );
  show(h("main", { class: "center" }, form));
  input.focus();
}

function signedIn(user: User): void {
  const signOut = h(
    "button",
    {
      type: "button",
      class: "secondary",
      onclick: async () => {
        signOut.disabled = true;
        try {
          await api.logout();
          loginPage("You're signed out.");
        } catch (err) {
          signOut.disabled = false;
          alert(messageOf(err));
        }
      },
    },
    "Sign out",
  );
  show(
    h(
      "header",
      { class: "bar" },
      h("strong", {}, "FeatureSteward"),
      h("span", { class: "who" }, `${user.handle} · ${user.role}`),
      signOut,
    ),
    h("main", {}, h("h1", {}, "Flags"), h("p", {}, "The flag list is coming in the next update.")),
  );
}

start();
