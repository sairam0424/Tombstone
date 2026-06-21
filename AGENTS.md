# Tombstone — Agent Coordination Rules

## Swarm Topology

| Task Type | Topology | Max Agents |
|-----------|----------|-----------|
| Feature development | hierarchical | 5 |
| Bug investigation | hierarchical | 3 |
| Cross-service refactoring | mesh | 8 |
| Research / exploration | fan-out | 10 |
| Security audit | pipeline | 4 |

## Agent Routing Table

| Task | Agent Type | Notes |
|------|-----------|-------|
| Go service implementation | Backend Architect | flag-api, gateway, evaluator |
| Python ML service | AI Engineer | intelligence service |
| TypeScript SDK | Backend Architect | ESM-only, strict mode |
| React dashboard | Frontend Developer | Vitest, no mock data in views |
| Proto schema design | Software Architect | source of truth |
| Security review | Security Engineer | auth, tokens, audit log |
| DB schema changes | Database Optimizer | sqlc regeneration required after |
| Performance work | Performance Benchmarker | benchmark before AND after |

## SendMessage-First Coordination

Named agents coordinate via `SendMessage`, not polling or shared state.

```
Lead ←→ researcher ←→ architect ←→ coder ←→ tester ←→ reviewer
```

When spawning a coordinated team — send ALL agents in ONE message:

```javascript
Agent({ name: "researcher", prompt: "Research X. SendMessage findings to 'architect'.", run_in_background: true })
Agent({ name: "architect",  prompt: "Wait for 'researcher'. Design solution. SendMessage to 'coder'.", run_in_background: true })
Agent({ name: "coder",      prompt: "Wait for 'architect'. Implement it. SendMessage to 'tester'.", run_in_background: true })
```

## Critical Rules

1. **Never** modify `go.work` without testing all services still compile.
2. **Never** mutate objects in the TypeScript cache — always spread to a new object.
3. **Never** write UPDATE or DELETE on `audit_log` table.
4. **Never** create a flag with a key that exists in `flag_tombstones`.
5. **Always** publish Redis pub/sub event after every flag state change.
6. **Always** write an audit entry after every flag state change.
7. **Proto changes** require regenerating Go stubs: `make gen-proto`.
8. **Schema changes** require regenerating sqlc types: `sqlc generate` in `services/flag-api/`.
