# CNCF Landscape Submission

**Requirement: 300 GitHub stars before submitting this PR.**
Current stars: 0 (as of 2026-06-27). Target date to submit: when stars reach 300.

## The Entry

Fork https://github.com/cncf/landscape and add to `landscape.yml` under the
"Feature Flagging" category (search for `flipt` or `unleash` to find the section):

```yaml
- item:
  name: Tombstone
  homepage_url: https://github.com/sairam0424/Tombstone
  logo: tombstone.svg
  repo_url: https://github.com/sairam0424/Tombstone
  project: ''
  description: Self-hosted production intelligence layer for feature flags — blast-radius gates, circuit-breaker auto-rollback, and causal incident correlation
  crunchbase: ''
```

Also add the SVG logo to `hosted_logos/tombstone.svg` — copy from `assets/logo-no-fill.svg`
but convert to the CNCF format (square, white/transparent background, no text).

## PR Details

**Title:** `add Tombstone to Feature Flagging`

**Body:**
```
Tombstone is a self-hosted production intelligence layer for feature flags.
It adds blast-radius scoring, circuit-breaker auto-rollback, and causal
incident correlation to the standard flag delivery model.

- GitHub: https://github.com/sairam0424/Tombstone
- License: MIT
- OpenFeature compatible: yes
- Self-hosted: yes (Docker Compose + Kubernetes operator)
```

## Checklist Before Submitting

- [ ] ≥ 300 GitHub stars
- [ ] Logo SVG prepared to CNCF specs (square, works on white)
- [ ] CLA signed (https://identity.linuxfoundation.org/projects/cncf)
- [ ] Verify "Feature Flagging" category exists at https://landscape.cncf.io/
