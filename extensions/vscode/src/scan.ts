// Finding flag keys in file contents. No VS Code dependencies, so it's
// tested on its own.

export interface Use {
  key: string;
  line: number;
  // start and end are the key's columns, without the quotes.
  start: number;
  end: number;
}

export const quoted = /(["'`])([a-z0-9][a-z0-9-]{0,99})\1/g;

// findUses returns every quoted string in text that is one of keys.
export function findUses(text: string, keys: ReadonlySet<string>): Use[] {
  if (keys.size === 0) return [];
  const uses: Use[] = [];
  const lines = text.split(/\r?\n/);
  for (let line = 0; line < lines.length; line++) {
    for (const m of lines[line].matchAll(quoted)) {
      if (keys.has(m[2])) uses.push({ key: m[2], line, start: m.index + 1, end: m.index + 1 + m[2].length });
    }
  }
  return uses;
}

// Files bigger than this, or with a NUL byte near the start, aren't source
// code worth scanning.
export const maxFileBytes = 1_000_000;

// File types, matching vscode.FileType.
export const fileType = { file: 1, symbolicLink: 64 } as const;

// scannable says whether a file from the workspace is safe to read: a
// regular file, not too big, and not a symlink. A repository could link
// to /dev/zero or a file outside the workspace.
export function scannable(stat: { type: number; size: number }): boolean {
  return stat.type === fileType.file && stat.size <= maxFileBytes;
}

export function looksLikeText(bytes: Uint8Array): boolean {
  return bytes.length <= maxFileBytes && !bytes.subarray(0, 8000).includes(0);
}
