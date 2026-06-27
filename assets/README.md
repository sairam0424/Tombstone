# Tombstone Assets

Brand assets for Tombstone — the self-hosted production intelligence layer for feature flags.

## Files

| File | Format | Size | Usage |
|------|--------|------|-------|
| `logo.svg` | SVG | 512×512 | Primary logo mark — dark fill, works on white backgrounds |
| `logo-no-fill.svg` | SVG | 512×512 | Stroke-only variant using `currentColor` — for OpenFeature ecosystem page, CNCF Landscape |
| `social-preview.svg` | SVG | 1280×640 | GitHub/Open Graph social preview — upload to GitHub Settings → Social preview (convert to PNG first) |

## Converting SVG → PNG

GitHub social preview requires PNG. Convert with:

```bash
# Using Inkscape (if installed):
inkscape --export-type=png --export-width=1280 --export-height=640 assets/social-preview.svg -o assets/social-preview.png

# Using rsvg-convert (brew install librsvg):
rsvg-convert -w 1280 -h 640 assets/social-preview.svg > assets/social-preview.png

# Using Node.js sharp:
node -e "require('sharp')('assets/social-preview.svg').resize(1280,640).toFile('assets/social-preview.png', console.log)"
```

## VS Code Extension Icon

The VS Code extension icon is at `workspace-vscode-ext/images/icon.svg`.
Convert to 128×128 PNG before running `vsce package`:

```bash
rsvg-convert -w 128 -h 128 workspace-vscode-ext/images/icon.svg > workspace-vscode-ext/images/icon.png
```

Then update `workspace-vscode-ext/package.json` → `"icon": "images/icon.png"` (already set).

## Usage Guidelines

- **Do not** stretch or distort the logo
- **Do not** add drop shadows or effects  
- **Do not** embed text in the logo (use as icon only, set text separately)
- On dark backgrounds: use `logo.svg` with `fill` inverted to `#f0f6fc` or white
- On light backgrounds: use `logo.svg` as-is (`fill="#1a1a2e"`)
