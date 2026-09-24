type Attr = string | number | boolean | ((event: Event) => void);

// h builds an element. Children that are strings become text nodes, so
// data from the API is never parsed as HTML.
export function h<K extends keyof HTMLElementTagNameMap>(
  tag: K,
  attrs: Record<string, Attr> = {},
  ...children: (Node | string)[]
): HTMLElementTagNameMap[K] {
  const el = document.createElement(tag);
  for (const [name, value] of Object.entries(attrs)) {
    if (typeof value === "function") {
      if (!name.startsWith("on")) throw new Error(`handler ${name} must start with "on"`);
      el.addEventListener(name.slice(2), value);
    } else if (typeof value === "boolean") {
      el.toggleAttribute(name, value);
    } else {
      el.setAttribute(name, String(value));
    }
  }
  el.append(...children);
  return el;
}
