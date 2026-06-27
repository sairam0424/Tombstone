# VS Code Extension Icon Required

The VS Code Marketplace requires a 128×128 PNG icon at this path:
`workspace-vscode-ext/images/icon.png`

Until this file exists, `vsce package` will fail with:
> ERROR: It seems the icon 'images/icon.png' doesn't exist.

## Design spec
- 128×128 pixels, PNG format
- Works on both light and dark VS Code themes
- Suggestion: Tombstone wordmark or tombstone stone icon on transparent/dark background
- Tool: Figma, Canva, or any image editor

Once created, place it at `workspace-vscode-ext/images/icon.png` and delete this file.
