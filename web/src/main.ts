import "./style.css";
import { Api, ApiError, type User } from "./api";
import { h } from "./dom";
import { environmentsPage, sdkKeysPage, tokensPage, userPage, usersPage } from "./admin";
import { flagPage } from "./flag";
import { newFlagPage, flagListPage } from "./flags";
import { atLeast, canReview, parseRoute, type Route } from "./format";
import { reviewsPage } from "./requests";

// App is what every page gets: the API, who is signed in, and helpers.
export interface App {
  api: Api;
  user: User;
  navigate(hash: string): void;
  // flash shows a message at the top of the next page.
  flash(message: string): void;
  // fail handles an unexpected error: an ended session goes back to sign in.
  fail(err: unknown): void;
  message(err: unknown): string;
  // refreshReviews updates the count of requests waiting for this user.
  refreshReviews(): void;
}

const api = new Api();
const root = document.getElementById("app")!;
let flashed = "";

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
  window.onhashchange = null;
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
  const main = h("main", { id: "main" });
  const app: App = {
    api,
    user,
    navigate: (hash) => {
      if (location.hash === hash) route();
      else location.hash = hash;
    },
    flash: (message) => {
      flashed = message;
    },
    fail: (err) => {
      if (err instanceof ApiError && err.status === 401) loginPage("Your session ended. Sign in again.");
      else main.replaceChildren(h("p", { class: "error", role: "alert" }, messageOf(err)));
    },
    message: messageOf,
    refreshReviews: () => {
      countReviews(api, user).then(
        (n) => {
          reviewsLink.textContent = n ? `Reviews (${n})` : "Reviews";
        },
        () => {}, // the count is a convenience; pages report real errors
      );
    },
  };

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
          app.fail(err);
        }
      },
    },
    "Sign out",
  );

  let current = 0;
  const route = async () => {
    const id = ++current;
    const r = parseRoute(location.hash);
    const notice = flashed;
    flashed = "";
    try {
      const nodes = await page(app, r);
      if (id !== current) return; // a newer navigation won
      markCurrent(nav, r.page);
      app.refreshReviews();
      main.replaceChildren(h("p", { class: "flash", role: "status" }, notice), ...nodes);
      // Move focus to the new page's heading, so screen readers announce it.
      main.querySelector<HTMLElement>("h1")?.focus();
    } catch (err) {
      if (id === current) app.fail(err);
    }
  };

  const links: [string, string, Route["page"][]][] = [
    ["#/flags", "Flags", ["flags", "new-flag", "flag"]],
    ["#/reviews", "Reviews", ["reviews"]],
    ["#/tokens", "Your tokens", ["tokens"]],
  ];
  if (atLeast(user.role, "admin")) {
    links.push(
      ["#/admin/users", "Users", ["users", "user"]],
      ["#/admin/sdk-keys", "SDK keys", ["sdk-keys"]],
      ["#/admin/environments", "Environments", ["environments"]],
    );
  }
  const nav = h(
    "nav",
    { "aria-label": "Main" },
    ...links.map(([href, text, pages]) => h("a", { href, "data-pages": pages.join(" ") }, text)),
  );
  const reviewsLink = nav.querySelector<HTMLElement>('a[href="#/reviews"]')!;

  show(
    h(
      "header",
      { class: "bar" },
      h("a", { class: "brand", href: "#/flags" }, "FeatureSteward"),
      nav,
      h("span", { class: "who" }, `${user.handle} · ${user.role}`),
      signOut,
    ),
    main,
  );
  window.onhashchange = route;
  route();
}

function page(app: App, r: Route): Promise<(Node | string)[]> | (Node | string)[] {
  switch (r.page) {
    case "flags":
      return flagListPage(app, r.params);
    case "new-flag":
      return newFlagPage(app);
    case "flag":
      return flagPage(app, r.key);
    case "tokens":
      return tokensPage(app);
    case "users":
      return usersPage(app);
    case "user":
      return userPage(app, r.handle);
    case "sdk-keys":
      return sdkKeysPage(app);
    case "environments":
      return environmentsPage(app);
    case "reviews":
      return reviewsPage(app);
    case "not-found":
      return [h("h1", { tabindex: "-1" }, "Page not found"), h("p", {}, h("a", { href: "#/flags" }, "Go to flags"))];
  }
}

// countReviews counts pending requests this user can review.
async function countReviews(api: Api, user: User): Promise<number> {
  const [pending, flags] = await Promise.all([api.requests({ status: "pending" }), api.flags()]);
  const stewards = new Map(flags.map((f) => [f.key, f.steward]));
  return pending.filter((r) => canReview(user, r, stewards.get(r.flag) ?? null)).length;
}

// markCurrent tells screen readers and the styles which nav link is open.
function markCurrent(nav: HTMLElement, current: Route["page"]): void {
  for (const a of nav.querySelectorAll("a")) {
    if (a.dataset.pages?.split(" ").includes(current)) a.setAttribute("aria-current", "page");
    else a.removeAttribute("aria-current");
  }
}

start();
