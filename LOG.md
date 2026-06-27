# Tombstone — Activity Log

Append-only journal of finished work, so anyone (human or agent) can catch up fast.
Newest first. Append an entry above older entries whenever a bulk of work wraps (ideally right before
the commit that ships it). Keep entries SHORT: header line + What + Refs, nothing else.

**Entry grammar** (strict, one header line per entry):
```
## YYYY-MM-DD · Short title · #tag1 #tag2
What: 1-2 lines, outcome first.
Refs: [doc](path) (new|updated), repo PR/commit links.
```

**Tags** (reuse before inventing):
#analysis #product #infra #loop #harness #incident #governance #rollout #signal #research

**Retrieval recipes** (macOS; entry headers always start `## 20`):
```bash
# index of all entries (one line each)
grep '^## 20' LOG.md
# last 5 entries, full
tail -r LOG.md | awk '{print} /^## 20/{c++; if(c==5) exit}' | tail -r
# all entries about a topic
awk '/^## 20/{p=/#incident/} p' LOG.md
# entries from a month
awk '/^## 20/{p=/^## 2026-06/} p' LOG.md
```

---

<!-- entries below this line, newest first -->

## 2026-06-27 · v1.0.0-local shipped — self-hosted release · #release
What: All 8 services running via make dev, React 19 dashboard all views functional, CONTRIBUTING/CODE_OF_CONDUCT/GitHub templates/GETTING_STARTED/GLOSSARY/Python+Ruby SDK READMEs added.
Refs: README.md (updated, v1.0.0 section), SECURITY.md (updated, self-hosted checklist), packages/sdks/flagmind-python/README.md (new), packages/sdks/flagmind-ruby/README.md (new).

## 2026-06-24 · v2.1.0 shipped — all 10 phases complete · #infra #loop #harness
What: Redis Streams (Phase 4.1), Slack HTTP routes + governance loop, mTLS (Phase 6.1), Argos LLM rule generation (Phase 3.2) merged to main. All 32 items from the 10-phase beast/ultimate upgrade plan are fully implemented.
Refs: Pull requests #44 #45 #46 #47 #48 merged. Tombstone v2.1.0 on main.

## 2026-06-24 · loop-engineer harness activated — 4 domain loops wired · #harness #loop #ops
What: ship-change.js workflow, /pr skill, /new-loop skill, dev-local launcher, and 4 domain loops (flag-cleanup daily, incident-response event-driven, rollout-advisor weekdays, governance weekly) all deployed to main.
Refs: .claude/workflows/ship-change.js (new), scripts/loop-*.sh (4 new), .github/workflows/loop-*.yml (4 new), domains/*/README.md (4 new).

## 2026-06-23 · Bootstrap loop-engineer knowledge base substrate · #harness #loop #infra
What: Created signals/, docs/, domains/ scaffolding plus LOG.md and ARCHITECTURE.md for Loop-Engineer v2 integration.
Refs: [signals/README.md](signals/README.md) (new), [docs/README.md](docs/README.md) (new), [domains/README.md](domains/README.md) (new), [ARCHITECTURE.md](ARCHITECTURE.md) (new)

## 2026-06-23 · incident-response: test-flag · #loop #incident
What: Circuit trip documented. Error rate: 0. Correlated: none.
Refs: docs/incident-2026-06-23-test-flag.md (new), domains/incident-response/metrics/trips.jsonl (updated).
