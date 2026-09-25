// Where flag-key completion applies. No VS Code dependencies, so it's
// tested on its own.

export interface OpenString {
  // start is the column after the opening quote; end is where the
  // key-shaped text around the cursor ends, so a completion replaces it.
  start: number;
  end: number;
  prefix: string;
  // text is the whole key-shaped text around the cursor.
  text: string;
}

// openStringAt returns the string literal the cursor at column ch is
// inside of, if what's typed so far could start a flag key. It only looks
// at this line, so it misses strings that span lines.
export function openStringAt(line: string, ch: number): OpenString | undefined {
  let quote = "";
  let start = 0;
  for (let i = 0; i < ch; i++) {
    const c = line[i];
    if (quote && c === "\\") {
      i++; // skip the escaped character
    } else if (quote ? c === quote : c === '"' || c === "'" || c === "`") {
      quote = quote ? "" : c;
      start = i + 1;
    }
  }
  if (!quote) return undefined;
  const prefix = line.slice(start, ch);
  if (!/^[a-z0-9-]*$/.test(prefix)) return undefined;
  const rest = /^[a-z0-9-]*/.exec(line.slice(ch))![0];
  return { start, end: ch + rest.length, prefix, text: prefix + rest };
}
