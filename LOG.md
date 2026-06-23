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

## 2026-06-23 · Bootstrap loop-engineer knowledge base substrate · #harness #loop #infra
What: Created signals/, docs/, domains/ scaffolding plus LOG.md and ARCHITECTURE.md for Loop-Engineer v2 integration.
Refs: [signals/README.md](signals/README.md) (new), [docs/README.md](docs/README.md) (new), [domains/README.md](domains/README.md) (new), [ARCHITECTURE.md](ARCHITECTURE.md) (new)
