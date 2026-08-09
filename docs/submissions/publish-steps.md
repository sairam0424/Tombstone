# Package Registry Publish Steps

Step-by-step commands for every registry. Do npm first (unblocks MCP Registry).

---

## 1. npm — @tombstone/* (7 packages)

**Prerequisites:**
1. Register at https://www.npmjs.com/signup
2. Enable 2FA (mandatory for publishing scoped packages)
3. Create @tombstone org scope: https://www.npmjs.com/org/create
4. Login: `npm login`

**Publish all packages:**
```bash
bash scripts/npm-publish.sh

# Or dry-run first:
bash scripts/npm-publish.sh --dry-run
```

**Verify:** https://www.npmjs.com/org/tombstone

---

## 2. MCP Registry (after npm publish)

```bash
# Install publisher CLI
brew install mcp-publisher   # or download from GitHub releases

# Authenticate
mcp-publisher login github   # device flow, uses sairam0424 GitHub account

# Publish from workspace-mcp/
cd workspace-mcp
mcp-publisher publish

# Verify
curl "https://registry.modelcontextprotocol.io/v0.1/servers?search=tombstone"
```

---

## 3. PyPI — flagmind Python SDK

```bash
cd packages/sdks/tombstone-python-sdk

# Install build tools
pip install build twine

# Build
python -m build
# Produces: dist/flagmind-0.1.0.tar.gz and dist/flagmind-0.1.0-py3-none-any.whl

# Test on TestPyPI first
python -m twine upload --repository testpypi dist/*
# Verify: https://test.pypi.org/project/flagmind/

# Publish to production PyPI
python -m twine upload dist/*
# Verify: https://pypi.org/project/flagmind/
```

**Note:** `flagmind` name was chosen because `tombstone` is taken on PyPI.

---

## 4. VS Code Marketplace

```bash
# Install vsce
npm install -g @vscode/vsce

# Register publisher at marketplace.visualstudio.com/manage/createpublisher
# Publisher name: tombstone

# Login
vsce login tombstone

# Package (verify it passes)
cd workspace-vscode-ext
vsce package
# Produces: tombstone-vscode-0.1.0.vsix

# Publish
vsce publish
# Or upload .vsix manually at marketplace.visualstudio.com/manage
```

**Verify:** https://marketplace.visualstudio.com/items?itemName=tombstone.tombstone-vscode

---

## 5. JetBrains Marketplace

```bash
cd workspace-jetbrains

# Build plugin
./gradlew buildPlugin
# Produces: build/distributions/Tombstone-Feature-Flags-0.1.0.zip

# Upload at: https://plugins.jetbrains.com/author/me
# → Add new plugin → Upload file → select the ZIP
```

---

## 6. Docker Hub (after ghcr.io images are live)

```bash
# Check ghcr.io images are published first:
docker pull ghcr.io/sairam0424/tombstone-flag-api:latest

# Create Docker Hub org 'tombstone' at app.docker.com
# Then extend docker-publish.yml to also push tombstone/* tags
# (see docs/SUBMISSION_GUIDE.md section 3.6 for workflow snippet)
```

---

## 7. ArtifactHub — Helm Chart

```bash
# Package chart
cd infra/helm
helm package flagmind/
# Produces: tombstone-0.1.0.tgz

# Push to ghcr.io OCI registry
helm push tombstone-0.1.0.tgz oci://ghcr.io/sairam0424/helm-charts

# Register at artifacthub.io
# → Add repository → Type: Helm (OCI) → URL: oci://ghcr.io/sairam0424/helm-charts
# ArtifactHub auto-indexes from the OCI registry
```

---

## 8. NuGet — .NET SDK

```bash
cd packages/sdks/tombstone-dotnet-sdk/src/FlagMind

# Build
dotnet pack --configuration Release

# Register at nuget.org and generate API key
dotnet nuget push Tombstone.Client.0.1.0.nupkg \
  --api-key $NUGET_API_KEY \
  --source https://api.nuget.org/v3/index.json
```

---

## 9. RubyGems — Ruby SDK

```bash
cd packages/sdks/tombstone-ruby-sdk

# Build gem (name is now flagmind-ruby — tombstone was taken)
gem build flagmind.gemspec
# Produces: flagmind-ruby-0.1.0.gem

# Register at rubygems.org
gem push flagmind-ruby-0.1.0.gem
```
