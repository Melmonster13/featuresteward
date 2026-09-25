# FeatureSteward for VS Code

See your [FeatureSteward](https://github.com/Melmonster13/featuresteward) feature flags, their stewards, and stale flags without leaving the editor.

## Features

- **Hover a flag key** in any quoted string to see its state in each environment, its steward, and whether it's stale or permanent, with a link to the dashboard.
- **Autocomplete flag keys** when you type a quote. Press Ctrl+Space inside the quotes to bring the list back; inside an existing key, it offers every flag to swap it for.
- **Stale Flags panel** in the Explorer: every flag that looks safe to remove, why, and each place your workspace uses it. Click a place to jump there. Uses of stale keys also get a hint in the editor.

The extension finds flag keys by name: any string in single quotes, double quotes, or backticks that matches a flag's key, in any language. It only reads; change flags in the dashboard or with `stew`, where production approvals apply.

## Getting started

1. Create an API token on your dashboard's **Your tokens** page. The extension only reads, so a token for a viewer is enough.
2. Run **FeatureSteward: Sign In** from the Command Palette, or click **FeatureSteward: signed out** in the status bar.
3. Enter your server's URL and paste the token.

The status bar shows how many flags are loaded; click it to refresh. Flags also refresh every minute.

## Security

- The token is kept in VS Code's secret storage (the system keychain), never in settings. It's tied to the URL you signed in to, so changing the URL signs you out.
- `featuresteward.url` can only be set in your user settings, so a repository's `.vscode/settings.json` can't send your token to another server.
- The token is only sent over `https`, or plain `http` to `localhost`, and never follows a redirect.
- Flag names and descriptions are shown as plain text: they can't add links that run commands.
- Signing out forgets the token but doesn't revoke it. Revoke it on the dashboard's **Your tokens** page.

## Settings

| Setting | Description | Default |
|---|---|---|
| `featuresteward.url` | Your FeatureSteward server, such as `https://flags.example.com`. User settings only. | — |
| `featuresteward.completion` | Suggest flag keys while typing inside quotes. | `true` |

## Commands

- **FeatureSteward: Sign In** / **Sign Out**
- **FeatureSteward: Refresh Flags**
- **FeatureSteward: Find Stale Flags**: search the workspace again for stale flags.

The search skips `node_modules`, `.git`, common build folders, binary files, and files over 1 MB.

## License

MIT
