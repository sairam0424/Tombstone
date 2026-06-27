# Getting Support

## Questions & General Help

For questions about using Tombstone, open a [GitHub Discussion](https://github.com/sairam0424/Tombstone/discussions). This is the best place for:
- "How do I set up X?"
- "What's the difference between X and Y?"
- "How does blast radius work?"

## Bug Reports

Found a bug? Open a [GitHub Issue](https://github.com/sairam0424/Tombstone/issues/new?template=bug_report.md) using the bug report template. Include:
- Tombstone version (`git tag | tail -1`)
- Steps to reproduce
- Expected vs actual behavior
- Service logs (`scripts/dev-local.sh logs <service>`)

## Feature Requests

Have an idea? Open a [GitHub Issue](https://github.com/sairam0424/Tombstone/issues/new?template=feature_request.md) using the feature request template.

## Security Vulnerabilities

**Do not open a public issue for security vulnerabilities.** See [SECURITY.md](SECURITY.md) for how to report them privately.

## Self-Hosted Troubleshooting

Before opening an issue, check:
1. [README.md Troubleshooting section](README.md#troubleshooting)
2. `scripts/dev-local.sh status` — verify all services are running
3. `scripts/dev-local.sh logs <service>` — check service logs

## Response Times

| Type | Expected Response |
|------|-----------------|
| Security vulnerability | 2 business days acknowledgement |
| Bug report | Best effort |
| Feature request | Best effort |
| Questions | Community-driven |
