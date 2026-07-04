# Security Policy

## Reporting a Vulnerability

**Do not open a public GitHub issue for security vulnerabilities.**

Report security issues to: **security@tombstone.io**

Include in your report:
- Description of the vulnerability and its potential impact
- Steps to reproduce (proof-of-concept if possible)
- Affected versions / components
- Any suggested mitigations

**Response SLA:**
- Acknowledgement within 2 business days
- Triage and severity assessment within 5 business days
- Patch + coordinated disclosure for confirmed Critical/High findings within 30 days

We follow [responsible disclosure](https://cheatsheetseries.owasp.org/cheatsheets/Vulnerability_Disclosure_Cheat_Sheet.html). Reporters who follow this policy will be credited in the release notes (unless they prefer anonymity).

---

## Software Bill of Materials (SBOM)

Every GitHub Release includes CycloneDX SBOMs for all SDK packages and the flag-api service:

| Artifact | File attached to release |
|----------|--------------------------|
| `@tombstone/core` (Node SDK) | `tombstone-core-sbom.json` |
| `@tombstone/react` (React SDK) | `tombstone-react-sbom.json` |
| `@tombstone/edge` (Edge SDK) | `tombstone-edge-sbom.json` |
| `tombstone-flag-api` (Go service) | `tombstone-flag-api-sbom.json` |

Each SBOM is signed with [cosign](https://github.com/sigstore/cosign) using keyless signing via GitHub OIDC. The corresponding `.bundle` file (e.g. `tombstone-core-sbom.bundle`) is also attached to the release.

### Verifying an SBOM signature

```bash
# Install cosign
brew install cosign     # macOS
# or: https://docs.sigstore.dev/cosign/installation/

# Download the SBOM and bundle from the GitHub release, then:
cosign verify-blob \
  --bundle tombstone-core-sbom.bundle \
  --certificate-identity-regexp "https://github.com/sairam0424/Tombstone/.*" \
  --certificate-oidc-issuer "https://token.actions.githubusercontent.com" \
  tombstone-core-sbom.json
```

A successful exit (status 0) confirms the SBOM was produced by this repository's CI pipeline and has not been tampered with.

---

## Dependency Update Policy

**Automated scanning (Dependabot):**
- Patch and minor updates are auto-merged when all CI checks pass.
- Major version bumps require manual review and a conventional commit `chore(deps):`.
- Security advisories with CVSS >= 7.0 are escalated to a priority fix within 7 days.

**Weekly audits:**
- Go: `govulncheck ./...` runs in CI on every push to `main`/`develop`.
- Node.js: `npm audit --audit-level=moderate` runs in CI.
- Python: `pip-audit` runs in CI for the intelligence service.
- Results are posted as PR comments and tracked in the security dashboard.

---

## SLSA Provenance

Tombstone achieves **SLSA Level 2** for all release artifacts.

| Requirement | How it is met |
|-------------|---------------|
| Version-controlled source | All source in GitHub; tags trigger releases |
| Authenticated builds | GitHub Actions OIDC identity; no self-hosted runners on release path |
| Hermetic Docker builds | `--mount=type=cache` build-kit cache mounts; `GOWORK=off` isolation per service |
| Signed provenance | `actions/attest-build-provenance@v1` generates signed SLSA provenance attestations on every CI build |
| SBOM attached to release | `syft` (CycloneDX JSON) + `cosign` bundle on every `v*` tag push |

### Verifying build provenance

```bash
# Requires GitHub CLI (gh) with the attestation extension
gh attestation verify <artifact> \
  --owner sairam0424 \
  --repo Tombstone
```

For further detail on SLSA: https://slsa.dev/spec/v1.0/levels

---

## Supported Versions

| Version | Supported |
|---------|-----------|
| Latest `main` | Yes |
| Last minor release | Security patches only |
| Older releases | No |
