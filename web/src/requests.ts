import { ApiError, type ChangeRequest, type Environment } from "./api";
import { h } from "./dom";
import { canReview, describeConfig, envState, flagHash } from "./format";
import type { App } from "./main";
import { confirmButton, formatTime } from "./ui";

// requestCard shows a change request with the actions this user may take.
// done runs after an action with a message for the next page.
export function requestCard(
  app: App,
  r: ChangeRequest,
  opts: { steward: string | null; envName: (k: string) => string; showFlag: boolean; done: (message: string) => void },
): HTMLElement {
  const env = opts.envName(r.environment);
  const error = h("p", { class: "error", role: "alert" });
  const act = async (fn: () => Promise<ChangeRequest>, message: (r: ChangeRequest) => string) => {
    error.textContent = "";
    try {
      opts.done(message(await fn()));
    } catch (err) {
      if (err instanceof ApiError && err.status === 401) return app.fail(err);
      error.textContent = app.message(err);
    }
  };

  let actions: Node | string = "";
  if (canReview(app.user, r, opts.steward)) {
    const commentId = `comment-${r.id}`;
    const comment = h("input", { id: commentId, autocomplete: "off", placeholder: "Optional" });
    const approve = h("button", { type: "button" }, "Approve");
    const reject = h("button", { type: "button", class: "secondary" }, "Reject");
    approve.addEventListener("click", () =>
      act(() => app.api.approve(r.id, comment.value.trim()), () => `Approved request #${r.id}. ${r.flag} in ${env} is now ${envState(r.proposed).label}.`),
    );
    reject.addEventListener("click", () =>
      act(() => app.api.reject(r.id, comment.value.trim()), () => `Rejected request #${r.id}.`),
    );
    actions = h(
      "div",
      { class: "inline-form" },
      h("div", { class: "field" }, h("label", { for: commentId }, "Comment"), comment),
      approve,
      reject,
    );
  } else if (r.status === "pending" && r.requested_by === app.user.handle) {
    actions = h(
      "div",
      { class: "actions" },
      confirmButton("Cancel request", "Confirm cancel", () => act(() => app.api.cancel(r.id), () => `Cancelled request #${r.id}.`), `Cancel request #${r.id}`),
    );
  } else if (r.status === "pending") {
    actions = h("p", { class: "hint" }, `Waiting for ${opts.steward ? `@${opts.steward} (the steward)` : "the flag's steward"} or an approver.`);
  }

  const title = opts.showFlag
    ? h("span", {}, h("a", { href: flagHash(r.flag) }, h("code", {}, r.flag)), ` in ${env}`)
    : h("span", {}, `Change request #${r.id}`);
  return h(
    "article",
    { class: `request ${r.status}`, "aria-label": `Request #${r.id} for ${r.flag} in ${env}` },
    h(
      "p",
      { class: "request-head" },
      title,
      " ",
      h("span", { class: `state ${r.status === "approved" ? "on" : r.status === "pending" ? "partial" : "off"}` }, capitalize(r.status)),
    ),
    h(
      "p",
      { class: "meta" },
      `#${r.id} by @${r.requested_by} · ${formatTime(r.created_at)}`,
      r.status === "pending" ? ` · expires ${formatTime(r.expires_at)}` : "",
      r.reviewed_by ? ` · ${r.status} by @${r.reviewed_by}` : "",
    ),
    r.reason ? h("p", { class: "quote" }, r.reason) : "",
    h(
      "dl",
      { class: "diff" },
      h("dt", {}, "Now"),
      h("dd", {}, describeConfig(r.base)),
      h("dt", {}, "Requested"),
      h("dd", {}, describeConfig(r.proposed)),
    ),
    r.review_comment ? h("p", { class: "quote" }, `@${r.reviewed_by}: ${r.review_comment}`) : "",
    actions,
    error,
  );
}

export async function reviewsPage(app: App): Promise<(Node | string)[]> {
  const [pending, recent, flags, envs] = await Promise.all([
    app.api.requests({ status: "pending" }),
    app.api.requests(),
    app.api.flags(),
    app.api.environments(),
  ]);
  const stewards = new Map(flags.map((f) => [f.key, f.steward]));
  const envName = (k: string) => envs.find((e: Environment) => e.key === k)?.name ?? k;
  const card = (r: ChangeRequest) =>
    requestCard(app, r, {
      steward: stewards.get(r.flag) ?? null,
      envName,
      showFlag: true,
      done: (message) => {
        app.flash(message);
        app.navigate("#/reviews");
      },
    });

  const forMe = pending.filter((r) => canReview(app.user, r, stewards.get(r.flag) ?? null));
  const mine = pending.filter((r) => r.requested_by === app.user.handle);
  const others = pending.filter((r) => !forMe.includes(r) && !mine.includes(r));
  const closed = recent.filter((r) => r.status !== "pending").slice(0, 20);

  const section = (title: string, empty: string, items: ChangeRequest[]) =>
    h("section", {}, h("h2", {}, `${title} (${items.length})`), items.length ? h("div", { class: "requests" }, ...items.map(card)) : h("p", { class: "muted" }, empty));

  return [
    h("h1", { tabindex: "-1" }, "Reviews"),
    h(
      "p",
      { class: "lead" },
      "Changes to protected environments need a second person: the flag's steward, an approver, or an admin. Requests expire after 7 days.",
    ),
    section("Waiting for you", "Nothing to review.", forMe),
    section("Your requests", "You have no pending requests.", mine),
    others.length ? section("Other pending requests", "", others) : "",
    section("Recently closed", "No closed requests yet.", closed),
  ];
}

function capitalize(s: string): string {
  return s.charAt(0).toUpperCase() + s.slice(1);
}
