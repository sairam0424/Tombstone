# Tombstone User Guide

**Audience:** Developers, product managers, and tech leads who are new to feature flags.
**Time to read:** ~25 minutes (or jump to the section you need).

---

## Table of Contents

1. [What Is a Feature Flag?](#1-what-is-a-feature-flag)
2. [Why Use Feature Flags?](#2-why-use-feature-flags)
3. [How Tombstone Works (2 min overview)](#3-how-tombstone-works-2-min-overview)
4. [The Dashboard — A Tour](#4-the-dashboard--a-tour)
5. [Step-by-Step: Your First Flag](#5-step-by-step-your-first-flag)
6. [Flag Lifecycle](#6-flag-lifecycle)
7. [Common Workflows](#7-common-workflows)
8. [SDK Integration Guide](#8-sdk-integration-guide)
9. [Understanding Blast Radius](#9-understanding-blast-radius)
10. [Troubleshooting Common Issues](#10-troubleshooting-common-issues)
11. [Keyboard Shortcuts](#11-keyboard-shortcuts)
12. [API Quick Reference](#12-api-quick-reference)
13. [Glossary Reference](#13-glossary-reference)

---

## 1. What Is a Feature Flag?

Imagine a light switch on your wall. You can flip it on or off at any moment — the wiring in the wall does not change, just whether the light is on.

A feature flag is that same light switch, but inside your code. You write the new feature, deploy it to production, and then flip a switch in a dashboard to turn it on when you are ready. Your code is already there. The flag controls whether users see it.

### Without a feature flag

You write new code, test it, and deploy it. The moment the deploy finishes, every user sees the change. If something goes wrong, you roll back — which can take 20+ minutes and risks introducing new bugs.

```typescript
// Old checkout — the only version that exists in production
function renderCheckout(cart: Cart) {
  return <OldCheckout cart={cart} />;
}
```

### With a feature flag

You ship both versions. The flag decides at runtime which one a user gets. Rollout is instant. Rollback is one toggle in a dashboard — no deploy required.

```typescript
import { useFlag } from "@tombstone/react";

function renderCheckout(cart: Cart) {
  const useNewCheckout = useFlag("checkout-v2", false);

  // false = safe default: show old checkout if flag is unreachable
  if (useNewCheckout) {
    return <NewCheckout cart={cart} />;
  }
  return <OldCheckout cart={cart} />;
}
```

**The key insight:** you separate *deploying code* from *releasing a feature*. Deploy happens on your schedule. Release happens when you decide — 5 minutes later, a week later, or to specific users only.

---

## 2. Why Use Feature Flags?

Here are the five situations where feature flags are the right tool:

### 1. Dark Launch — ship code before users see it

Deploy a half-built feature to production with the flag off. Continue building. Flip the flag on the day you are ready, no emergency deploy needed. The code has already been running (and not crashing) in production for a week.

### 2. Canary Release — show to 5% of users first

Roll out to 1% of users, watch your error rate and dashboards for 30 minutes, then ramp to 10%, 50%, and finally 100%. If something breaks at 5%, you affect 5% of users — not everyone.

### 3. Kill Switch — turn off a broken feature in 10 seconds

Production is on fire. Instead of rolling back a deploy (20+ minutes, risk of new bugs), you open the dashboard, find the flag, and toggle it off. Done in under 10 seconds. The broken code is still there — it is just invisible to users until you fix it.

### 4. A/B Testing — show two versions, measure which converts better

50% of users see the blue button, 50% see the green button. Tombstone's Experiments view connects flag evaluation data to your data warehouse and tells you which variant wins — with statistical confidence, not a gut feeling.

### 5. Access Control — enable for beta users or specific teams only

Enable a feature only for users in your `beta` cohort or for employees in your `internal` org. When the feature is ready for everyone, change the targeting rule — not the code.

---

## 3. How Tombstone Works (2 min overview)

At its core, Tombstone answers one question for your code: "Is this flag on for this user, right now?"

```
Your App
    |
    | isEnabled("checkout-v2", userContext)
    v
+------------------+
|   flag-api       |  <-- stores flag definitions, rollout %, rules
|   :8081          |      (PostgreSQL behind the scenes)
+------------------+
    |
    | true / false
    v
Your App renders the right experience

Meanwhile, in the background:
+------------------+     +------------------+     +------------------+
|   gateway        |     |   evaluator      |     |   intelligence   |
|   :8080          |     |   :8082          |     |   :8083          |
|                  |     |                  |     |                  |
| Streams live     |     | Watches error    |     | Detects anomalies|
| flag updates to  |     | rates. If a flag |     | recommends safe  |
| your SDK via SSE |     | causes >5% errors|     | rollout speeds   |
| (no poll needed) |     | it auto-rolls    |     | and flags that   |
|                  |     | back the flag    |     | look stale       |
+------------------+     +------------------+     +------------------+
```

**dashboard** at `localhost:3000` is the human interface where you create flags, adjust rollout percentages, review approvals, and investigate incidents.

**flag-api** at `localhost:8081` is the REST API — the dashboard and your SDKs both talk to it.

**gateway** at `localhost:8080` is the real-time update channel — when you change a flag in the dashboard, your SDK receives the update in milliseconds without polling.

**evaluator** at `localhost:8082` is the safety layer — it computes how risky a flag change is (Blast Radius) and will automatically roll back a flag if error rates spike.

**intelligence** at `localhost:8083` is the AI layer — anomaly detection, rollout recommendations, and experiment analysis.

---

## 4. The Dashboard — A Tour

Open `http://localhost:3000` to see the dashboard. Here is every view and what it does.

### All Flags (`/flags`)

Your flag library. Every flag you have ever created appears here. Use `Cmd+K` to search — type a flag key or any keyword and the results filter instantly.

**Columns:** Key, Name, Type (BOOLEAN/STRING/NUMBER/JSON), Status, Environment states, Last modified.

**When to use it:** Starting your day, looking up a specific flag, bulk operations (archive multiple flags at once).

**Filters:** Use the top bar to filter by environment, flag type, status (active / archived / stale), or blast radius level.

### Flag Detail (`/flags/:key`)

The single-flag control panel. Click any flag in the All Flags view to open it.

**Environment tabs** — Every flag is independent per environment. Production can be at 10% rollout while development is at 100%. Click the tab for the environment you want to control.

**Rollout % card** — Shows the current rollout percentage for the selected environment. Click the percentage number to open a slider. Drag to your target value (or type it). Click Save.

**Enabled toggle** — The master on/off for this flag in this environment. A flag at 100% rollout but toggled off returns false for every user. Toggle on to activate.

**Audit log** — At the bottom of the page. Shows every change: who changed it, what they changed it from and to, and the exact timestamp. Append-only — nobody can edit or delete these entries.

**Prerequisites tab** — Lists flags that must be on before this flag can turn on. Useful for feature dependencies ("new cart UI" requires "new cart service" to be enabled first).

**Variations tab** — For non-boolean flags. Lets you define named variants (e.g., `blue`, `green`, `red`) and set the traffic weight for each.

**Circuit Breaker status badge** — Red if the evaluator has auto-rolled back this flag due to an error spike. Green means healthy. Orange means approaching threshold.

**When to use it:** Any time you are changing a flag's rollout, investigating its history, or configuring advanced rules.

### Approvals (`/approvals`)

The four-eyes workflow queue. When someone on your team requests a change to a production flag instead of saving directly, a change request appears here.

**Each card shows:** Who requested it, which flag, what they want to change, when they requested it, and their description of expected impact.

**Actions:** Approve (the change applies immediately) or Reject (sends notification back to the requestor with your reason).

**When to use it:** Start your day here if you are an approver. Any team member should check here when they are waiting for a change to go through.

### Break-Glass (`/settings` → SDK Tokens section)

Emergency override tokens for on-call incidents. When normal approval workflows would take too long and production is on fire, break-glass lets you act immediately.

A break-glass token is:
- Scoped to specific flags and environments (or `*` for all)
- Time-limited (expires automatically — typically 1–8 hours)
- Tied to an incident reference number
- Fully logged in the audit trail

Every action taken with a break-glass token is visible in the audit log with a `[BREAK-GLASS]` marker.

**When to use it:** 3am incident. Your teammate is asleep. You need to disable a flag right now. See the full workflow in [Break-Glass Emergency](#break-glass-emergency).

### What Changed? (`/incidents`)

The incident timeline. When something breaks in production, this is your first stop.

Tombstone correlates every flag state change with the timestamp you provide. You type "production went red at 14:32" and Tombstone shows you every flag that changed in the 30-minute window around that time, ranked by proximity and blast radius.

**When to use it:** Any production incident. "What changed right before the alert fired?" This view answers that question in seconds, not minutes of log hunting.

### Causal Graph (`/blast-radius` → Dependency view)

A visual map of which flags depend on which other flags. If Flag B has Flag A as a prerequisite, you see an arrow from A to B.

This is important when disabling a high-level flag — you can see at a glance which other flags will also stop working because their prerequisite is gone.

**When to use it:** Before disabling a heavily-depended-on flag. Before a major rollout that involves several related flags. When planning cleanup of a family of flags.

### Governance (`/governance`)

Your flag hygiene dashboard. Tombstone watches all your flags and tells you:

- **Stale flags** — flags that have not been evaluated in 30+ days (likely dead code you forgot about)
- **Health score** — an overall score (0–100) for your flag estate. Lower score = more stale/risky flags
- **Autonomous rollout recommendations** — the intelligence service analyzes error rates and anomaly signals and recommends safe rollout increases or decreases
- **SOC2 compliance evidence** — exportable audit trail summaries for security reviews

**When to use it:** Weekly review. Before a compliance audit. When the flag-cleanup loop fires a signal that stale flag count is above threshold.

### Experiments (`/experiments`)

A/B test results connected to your data warehouse. Tombstone supports BigQuery, Snowflake, and Databricks as warehouse connectors.

Each experiment shows:
- Which flag is the treatment variable
- Variant breakdown (how many users in each bucket)
- CUPED-adjusted metrics (20–40% variance reduction for faster statistical significance)
- Sequential testing status (mSPRT — the test tells you when it is safe to stop, so you do not peek too early)
- Collision detection warnings (if two experiments overlap the same user segments)

**When to use it:** After running a flag for A/B traffic for several days. When deciding whether to ship a variant or roll back.

### Marketplace (`/marketplace`)

Connect Tombstone to the tools your team already uses. Available integrations:

| Integration | What it does |
|-------------|-------------|
| **Slack** | Posts alerts when a circuit breaker trips, a stale flag is detected, or a change request needs approval |
| **Datadog** | Sends flag evaluation metrics as custom metrics; adds flag change events to your Datadog event stream |
| **PagerDuty** | Creates incidents automatically when the circuit breaker rolls back a flag |
| **OpsGenie** | Same as PagerDuty — creates alerts on circuit breaker events |
| **Jira** | Creates Jira tickets for flag cleanup tasks from the Governance view |
| **Linear** | Same as Jira for teams using Linear |
| **OpenTelemetry** | Exports flag evaluation traces to your OTel collector for distributed tracing correlation |

**When to use it:** During initial setup. When adding a new tool to your stack. When the team complains they are not getting notified about flag incidents.

---

## 5. Step-by-Step: Your First Flag

This walkthrough covers every click from zero to a working flag in your code.

### Prerequisites

- `make dev` has been run and all services are healthy
- You have `http://localhost:3000` open in your browser
- You have `http://localhost:8081` available (flag-api)

### Step 1 — Open the dashboard

Navigate to `http://localhost:3000`.

You should see the All Flags view with some sample flags pre-loaded. If you see a blank list with a "Start by creating your first flag" message, that is fine too — you are about to create one.

### Step 2 — Open the New Flag form

Click the **+ New Flag** button in the top-right corner of the screen.

Alternatively: press `Cmd+K` (Mac) or `Ctrl+K` (Windows/Linux) to open the command palette, type `new flag`, and press Enter.

The Create Flag form slides in from the right.

### Step 3 — Fill in the flag details

You will see four fields:

**Key** — type `my-first-flag`

This is the permanent, immutable identifier for your flag. Your code will reference this string forever. Rules:
- Lowercase only
- Hyphens allowed, no spaces, no underscores, no capital letters
- Once you create the flag, this key can never change or be reused — even after archiving

Why can it not be reused? This is the Knight Capital rule. In 2012, Knight Capital lost $440 million in 45 minutes because engineers reused an old flag key. New code picked up a stale value from the old key and executed unintended trades. Tombstone blocks key reuse at the database level so this class of bug is impossible.

**Name** — type `My First Flag`

This is the human-readable display name. It can be changed any time. Put something descriptive here.

**Description** — type `Testing Tombstone for the first time`

Optional but helpful for your teammates and for the Governance view.

**Type** — select `BOOLEAN`

Tombstone supports four flag types:
- `BOOLEAN` — true or false. Use this for 90% of flags (feature on/off, kill switch, canary).
- `STRING` — returns one of several string values. Use for A/B test variant names or config values.
- `NUMBER` — returns a numeric value. Use for rate limits, timeouts, or numerical experiments.
- `JSON` — returns a JSON object. Use for complex configuration that would otherwise require a deploy.

For your first flag, `BOOLEAN` is right.

### Step 4 — Create the flag

Click **Create Flag**.

The form closes and you land on the Flag Detail page for `my-first-flag`.

You will notice the flag exists in all three environments (development, staging, production) but is disabled and at 0% rollout in all of them. It does nothing yet.

### Step 5 — Enable the flag in development

You should already be on the **development** tab. If not, click the `development` tab near the top of the flag detail page.

**Set rollout to 100%:**
Click the rollout percentage card (it shows `0%`). A slider appears. Drag it all the way to the right to `100%`, or click the number field and type `100`. Click **Save**.

**Toggle the flag on:**
Find the Enabled toggle (it should say `Disabled`). Click it. It flips to `Enabled`.

You will see a toast notification: "Flag updated successfully."

### Step 6 — Verify the flag is live

Scroll to the bottom of the Flag Detail page. You will see the **Audit Log** section.

It should show two entries:
1. Rollout changed from `0%` to `100%` — by `system` (your session), just now.
2. Status changed from `disabled` to `enabled` — by `system`, just now.

This is the immutable record. Every change is here, forever, with a timestamp.

### Step 7 — Use it in your code

Open a terminal and run this curl command to confirm the flag is returning `true`:

```bash
curl -s \
  -H "Authorization: Bearer sdk-dev-token-change-in-prod" \
  "http://localhost:8081/api/v1/flags/my-first-flag/evaluate?environment=development" \
  | jq .
```

You should see:

```json
{
  "value": true,
  "reason": "FULL_ROLLOUT",
  "flag_key": "my-first-flag",
  "environment": "development"
}
```

Your flag is live. Now you can use it in real code — see [SDK Integration Guide](#8-sdk-integration-guide) for complete examples.

---

## 6. Flag Lifecycle

Every flag moves through a predictable set of stages. Understanding the lifecycle helps you know what to do (and what not to do) at each point.

```
         You create the flag
                 |
                 v
+----------------------------------------+
|  DRAFT                                 |
|  enabled: false  rollout: 0%           |
|  Flag exists in database.              |
|  No users are affected.                |
|  Safe to configure and test.           |
+----------------------------------------+
                 |
     Enable in staging, test your code
                 |
                 v
+----------------------------------------+
|  ACTIVE (enabled: false, rollout: 0%)  |
|  Flag is deployed to production.       |
|  Code references the flag.             |
|  Still serving false to everyone.      |
|  This is "dark launch" — shipped but   |
|  invisible.                            |
+----------------------------------------+
                 |
      Enable in production at 1%
                 |
                 v
+----------------------------------------+
|  ROLLING OUT (enabled: true, 1-99%)    |
|  Real users are seeing the feature.    |
|  Monitor error rates and metrics.      |
|  Increase % slowly: 1 → 10 → 50 → 100 |
|  Circuit breaker watching for spikes.  |
+----------------------------------------+
                 |
      Rollout reaches 100%, stable 7+ days
                 |
                 v
+----------------------------------------+
|  FULL ROLLOUT (enabled: true, 100%)    |
|  All users see the feature.            |
|  Flag is still in your code.           |
|  Schedule cleanup — this flag's job    |
|  is done. Start the CLEANUP stage.     |
+----------------------------------------+
                 |
   Remove flag from code via ast-rewriter
                 |
                 v
+----------------------------------------+
|  CLEANUP                               |
|  Code references removed.              |
|  Flag still exists in dashboard.       |
|  Tombstone the flag key to prevent     |
|  future reuse.                         |
+----------------------------------------+
                 |
       Click Archive in dashboard
                 |
                 v
+----------------------------------------+
|  ARCHIVED / TOMBSTONED                 |
|  Flag key is permanently blocked from  |
|  reuse (Knight Capital prevention).    |
|  Audit history preserved forever.      |
|  Appears in /tombstones view.          |
+----------------------------------------+
```

**What to do at each stage:**

| Stage | Action |
|-------|--------|
| DRAFT | Configure type, description, safe default. Test in development. |
| ACTIVE | Deploy your code. Enable the flag in staging. Run integration tests. |
| ROLLING OUT | Increase rollout gradually. Watch dashboards. Stay ready to disable. |
| FULL ROLLOUT | Monitor for 7+ days. Confirm no incidents. Schedule cleanup. |
| CLEANUP | Run `ast-rewriter` to remove dead code. Open a PR. Review the diff. |
| ARCHIVED | Done. The key is tombstoned. |

---

## 7. Common Workflows

### Canary Release

**Goal:** Roll out a risky change to a small percentage of users first, monitor, then expand.

**The scenario:** You have built a new checkout flow. It works in staging. But checkout is the most important page in your product and you do not want to bet everyone on it at once.

**Step 1 — Create the flag**

Key: `checkout-v2`
Type: `BOOLEAN`
Description: `New checkout flow — redesigned cart, faster payment processing`

**Step 2 — Enable at 1% in production**

On the Flag Detail page, switch to the `production` environment tab.
Set rollout to `1%`. Click Save.
Toggle Enabled. Click Save.

The flag is now live for roughly 1 in 100 production users.

**Step 3 — Monitor (30 minutes)**

Watch your error rate dashboards, Datadog, or whatever observability tool you use. Watch for:
- Increased error rates on checkout-related endpoints
- Payment failure rates
- Drop in conversion rate

The Tombstone evaluator is also watching. If errors spike above 5% on 100 requests, it will auto-rollback.

**Step 4 — Increase to 10%**

If your 30-minute monitoring window shows no degradation, move the rollout slider to `10%`. Click Save.

Monitor again.

**Step 5 — Ramp up**

```
1% (30 min) → 10% (1 hour) → 50% (2 hours) → 100%
```

Adjust cadence based on your traffic volume. Low-traffic services may need longer windows. High-traffic services get signal faster.

**Step 6 — Archive the flag**

Once you have been at 100% for 7+ days with no incidents, schedule cleanup.

Open Governance, find `checkout-v2` in the Stale Flags list (it will appear after 30 days at 100%), and follow the cleanup steps.

---

### Kill Switch

**Goal:** Turn off a broken feature in production without a rollback deploy.

**The scenario:** Your new payment processing integration has a bug. Transactions are failing for 8% of users. You need to stop the bleeding right now.

**Normal rollback:** 20+ minutes. Risk of new bugs in the rollback. Requires a deploy pipeline run.

**With a kill switch flag:** 10 seconds.

**Step 1 — Open the dashboard**

Navigate to `http://localhost:3000`.

Press `Cmd+K` and type the flag name, or scroll to find it in All Flags.

**Step 2 — Go to the flag detail**

Click the flag `payment-processor-v2` (or whatever key you used).

**Step 3 — Disable in production**

Make sure the `production` tab is selected.

Find the Enabled toggle (currently showing `Enabled`). Click it. It flips to `Disabled`.

Click **Save Changes**.

**Done.** In milliseconds, the gateway streams this update to every SDK connected to your production environment. All users fall back to the old payment processor. The broken code is still deployed — it is just unreachable.

**Recovery:** Fix the bug. Deploy the fix. Toggle the flag back on. Ramp up gradually.

**Comparison:**

```
Kill switch:  10 seconds to disable, 0 risk of new bugs
Deploy rollback: 20+ minutes, requires CI pipeline, risk of introducing
                 new issues in the rollback commit
```

---

### Four-Eyes Approval

**Goal:** Require a second human to approve any change to a production flag that touches a critical path (payments, auth, user data).

**When to use it:** Any time a single person making a flag change unilaterally is too risky. The payment team uses this for every production change. The auth team uses it for anything above 10% rollout.

**Step 1 — Make your change, but use Request Change instead of Save**

On the Flag Detail page, adjust the rollout or toggle. Instead of clicking the normal **Save Changes** button, click **Request Change** (the secondary button).

A form appears asking for:
- Description of the change
- Expected impact
- Risk assessment

Fill it in honestly. Your approver will read this.

**Step 2 — Approver gets notified**

If you have Slack connected, the approver receives a Slack message immediately. Otherwise, they see it next time they open the dashboard at `/approvals`.

**Step 3 — Approver reviews and approves**

The approver opens the change request. They see the before state, the after state, your description, and the blast radius assessment.

If they approve: the change is applied automatically, and the audit log records both the request and the approval.

If they reject: you receive a notification. The flag is unchanged. You can revise and resubmit.

**Who can approve?** Only users with the `approver` role. Roles are managed in Settings. If your change request is stuck in pending, someone with admin access needs to check whether the right people have the approver role.

---

### Break-Glass Emergency

**Goal:** Override the approval workflow during a live production incident at 3am.

**The scenario:** It is 3:17am. PagerDuty woke you up. Production error rate is at 12% and climbing. You have identified the flag. Your teammate (the only approver) is not responding. Every minute costs revenue.

**Step 1 — Navigate to Settings → SDK Tokens**

Go to `http://localhost:3000/settings` and find the **SDK Tokens** section.

**Step 2 — Create a Break-Glass Token**

Click **Create Break-Glass Token**.

Fill in:
- **Scope:** Select the specific flag key (`payment-processor-v2`) and environment (`production`). For broader incidents, you can use `*` to scope to all flags, but prefer the smallest scope that solves the problem.
- **Expiry:** Set to 1 hour (you can extend it if the incident runs long, but start small).
- **Incident reference:** Enter your PagerDuty incident number, a Jira ticket, or any reference string. This is recorded in the audit trail.

Click **Create Token**.

**Important:** Copy the token immediately. It is shown once. If you lose it, create a new one.

**Step 3 — Use the token**

You can use this token in a curl call or directly in your SDK. Here is the curl version to disable the flag right now:

```bash
curl -X PATCH \
  -H "Authorization: Bearer break-glass-token-xxxxxxxx" \
  -H "Content-Type: application/json" \
  -d '{"enabled": false, "rollout_percentage": 0}' \
  "http://localhost:8081/api/v1/flags/payment-processor-v2/environments/production"
```

The flag is disabled. The gateway streams the update. Error rate drops.

**Step 4 — Clean up after the incident**

The token expires automatically at the time you set. You do not need to revoke it manually (though you can).

Write a post-mortem. The audit log already has the full record: who created the token, when, what it was scoped to, what changes were made with it.

---

### Cleaning Up Old Flags

**Goal:** Remove dead flag code from your codebase and tombstone the key to prevent accidental reuse.

Why does this matter? Tombstone is named after the tombstoning pattern precisely because stale flags are dangerous. The Knight Capital incident (2012, $440M in 45 minutes) was caused by a flag key that was supposed to be dead but was accidentally reused. New code picked up the stale value and executed 8 days worth of trades in 45 minutes.

**Step 1 — Find stale flags**

Open Governance (`/governance`). Under **Stale Flags**, you will see flags that have been at 100% rollout for 30+ days — they are candidates for cleanup.

**Step 2 — Confirm the flag is truly done**

Before archiving, confirm:
- The flag is at 100% in all environments
- No experiments are running on it
- No other flags list it as a prerequisite
- The feature has been stable for at least 7 days

**Step 3 — Set rollout to 0% and disable**

Before removing code references, set the flag to `0% / disabled`. This ensures that if any code reference remains (even after the rewriter runs), it falls back to the safe default.

**Step 4 — Run ast-rewriter to remove code references**

```bash
# List all code references to this flag key
curl -H "Authorization: Bearer sdk-dev-token-change-in-prod" \
  "http://localhost:8085/api/v1/scan?flag_key=checkout-v2"

# Rewrite — removes isEnabled("checkout-v2") calls, replaces with the safe default value
curl -X POST \
  -H "Authorization: Bearer sdk-dev-token-change-in-prod" \
  -H "Content-Type: application/json" \
  -d '{"flag_key": "checkout-v2", "default_value": true}' \
  "http://localhost:8085/api/v1/rewrite"
```

The ast-rewriter uses jscodeshift under the hood. It finds every call to `isEnabled`, `useFlag`, `getBooleanValue`, and similar SDK methods that references your flag key, and replaces them with the literal value (the safe default you pass in).

**Step 5 — Open a PR and review the diff**

The rewriter modifies your source files in-place. Review the diff carefully. The PR should show nothing but the removal of flag conditionals — the "enabled" branch of every `if` stays, the `else` branch is removed.

**Step 6 — Tombstone the key**

After the PR merges, open the Flag Detail page for `checkout-v2`. Click **Archive Flag**. When prompted, confirm you want to tombstone the key permanently.

The key now appears in `/tombstones`. It can never be used again. Future flag creation will reject any attempt to use `checkout-v2` as a key.

---

## 8. SDK Integration Guide

### TypeScript / Node.js

**Install:**

```bash
npm install @tombstone/core
```

**Basic setup:**

```typescript
import { TombstoneClient } from "@tombstone/core";

const client = new TombstoneClient({
  apiUrl: "http://localhost:8081",
  sdkToken: process.env.TOMBSTONE_SDK_TOKEN ?? "sdk-dev-token-change-in-prod",
  environment: process.env.NODE_ENV === "production" ? "production" : "development",
});

// Initialize once at startup — fetches all flags and subscribes to live updates
await client.initialize();
```

**Checking a boolean flag:**

```typescript
// Second argument is the safe default — returned if flag-api is unreachable
const enabled = await client.getBooleanValue("checkout-v2", false);

if (enabled) {
  // new code path
} else {
  // old code path
}
```

**Passing user context for targeting rules:**

```typescript
const context = {
  userId: "user-12345",
  orgId: "org-acme",
  region: "us-east-1",
  plan: "enterprise",
  betaUser: true,
};

const enabled = await client.getBooleanValue("checkout-v2", false, context);
```

Context lets you target flags at specific users, organizations, regions, or any attribute you add. A flag with a targeting rule like "only for users where `betaUser: true`" will return `true` for beta users and the safe default for everyone else.

**String / Number / JSON flags:**

```typescript
// String flag — e.g. A/B test variant name
const variant = await client.getStringValue("checkout-variant", "control");
// Returns "control", "blue-cta", "simplified-form", etc.

// Number flag — e.g. rate limit
const rateLimit = await client.getNumberValue("api-rate-limit", 100);

// JSON flag — e.g. complex config object
const config = await client.getJsonValue("pricing-config", { tier: "default" });
```

**Express middleware — feature gating a route:**

```typescript
import express from "express";
import { TombstoneClient } from "@tombstone/core";

const app = express();
const tombstone = new TombstoneClient({
  apiUrl: process.env.TOMBSTONE_API_URL!,
  sdkToken: process.env.TOMBSTONE_SDK_TOKEN!,
  environment: "production",
});

await tombstone.initialize();

// Middleware: gate the new checkout API behind a flag
app.use("/api/v2/checkout", async (req, res, next) => {
  const enabled = await tombstone.getBooleanValue("checkout-v2", false, {
    userId: req.user?.id,
    orgId: req.user?.orgId,
  });

  if (!enabled) {
    return res.status(404).json({ error: "Not found" });
  }

  next();
});
```

**Shutdown gracefully:**

```typescript
// Call this when your process is stopping to close SSE connection cleanly
await client.close();
```

---

### Python

**Install:**

```bash
pip install httpx  # synchronous HTTP
# or
pip install httpx[asyncio]  # async
```

**Basic setup (synchronous):**

```python
import os
import httpx

TOMBSTONE_URL = os.getenv("TOMBSTONE_API_URL", "http://localhost:8081")
TOMBSTONE_TOKEN = os.getenv("TOMBSTONE_SDK_TOKEN", "sdk-dev-token-change-in-prod")
TOMBSTONE_ENV = os.getenv("TOMBSTONE_ENVIRONMENT", "development")

def is_enabled(flag_key: str, default: bool = False, user_id: str = None) -> bool:
    try:
        params = {"environment": TOMBSTONE_ENV}
        if user_id:
            params["context[userId]"] = user_id

        response = httpx.get(
            f"{TOMBSTONE_URL}/api/v1/flags/{flag_key}/evaluate",
            headers={"Authorization": f"Bearer {TOMBSTONE_TOKEN}"},
            params=params,
            timeout=2.0,  # always set a timeout — flag-api should never block your app
        )
        response.raise_for_status()
        return response.json().get("value", default)
    except Exception:
        return default  # safe default on any error
```

**Async usage (asyncio / FastAPI):**

```python
import asyncio
import os
import httpx
from functools import lru_cache

TOMBSTONE_URL = os.getenv("TOMBSTONE_API_URL", "http://localhost:8081")
TOMBSTONE_TOKEN = os.getenv("TOMBSTONE_SDK_TOKEN", "sdk-dev-token-change-in-prod")
TOMBSTONE_ENV = os.getenv("TOMBSTONE_ENVIRONMENT", "development")

async def is_enabled_async(
    flag_key: str,
    default: bool = False,
    user_id: str | None = None,
) -> bool:
    async with httpx.AsyncClient(timeout=2.0) as client:
        try:
            params = {"environment": TOMBSTONE_ENV}
            if user_id:
                params["context[userId]"] = user_id

            response = await client.get(
                f"{TOMBSTONE_URL}/api/v1/flags/{flag_key}/evaluate",
                headers={"Authorization": f"Bearer {TOMBSTONE_TOKEN}"},
                params=params,
            )
            response.raise_for_status()
            return response.json().get("value", default)
        except Exception:
            return default
```

**FastAPI middleware — gate a route:**

```python
from fastapi import FastAPI, Request, HTTPException
from fastapi.middleware.base import BaseHTTPMiddleware

app = FastAPI()

class FeatureFlagMiddleware(BaseHTTPMiddleware):
    async def dispatch(self, request: Request, call_next):
        if request.url.path.startswith("/api/v2/checkout"):
            user_id = request.headers.get("X-User-Id")
            enabled = await is_enabled_async("checkout-v2", default=False, user_id=user_id)
            if not enabled:
                raise HTTPException(status_code=404, detail="Not found")
        return await call_next(request)

app.add_middleware(FeatureFlagMiddleware)

@app.get("/api/v2/checkout")
async def new_checkout():
    return {"message": "New checkout"}
```

---

### React

**Install:**

```bash
npm install @tombstone/react @tombstone/core
```

**Wrap your app with TombstoneProvider:**

```tsx
import React from "react";
import { TombstoneProvider } from "@tombstone/react";

function App() {
  return (
    <TombstoneProvider
      apiUrl={import.meta.env.VITE_TOMBSTONE_API_URL ?? "http://localhost:8081"}
      sdkToken={import.meta.env.VITE_TOMBSTONE_SDK_TOKEN ?? "sdk-dev-token-change-in-prod"}
      environment={import.meta.env.VITE_TOMBSTONE_ENV ?? "development"}
      context={{
        // User context — update this when the user logs in
        userId: currentUser?.id,
        orgId: currentUser?.orgId,
        plan: currentUser?.plan,
      }}
    >
      <Router>
        <Routes />
      </Router>
    </TombstoneProvider>
  );
}
```

**useFlag hook — the simplest way:**

```tsx
import { useFlag } from "@tombstone/react";

function CheckoutPage() {
  // Second argument is the safe default
  const useNewCheckout = useFlag("checkout-v2", false);

  if (useNewCheckout) {
    return <NewCheckoutFlow />;
  }

  return <OldCheckoutFlow />;
}
```

**Feature flag as a loading gate:**

While the SDK initializes (first render before flags are fetched), show a skeleton instead of flashing the wrong content.

```tsx
import { useFlag, useFlagsReady } from "@tombstone/react";

function CheckoutPage() {
  const ready = useFlagsReady();
  const useNewCheckout = useFlag("checkout-v2", false);

  if (!ready) {
    // SDK is connecting to flag-api — show skeleton to avoid flash
    return <CheckoutSkeleton />;
  }

  return useNewCheckout ? <NewCheckoutFlow /> : <OldCheckoutFlow />;
}
```

**Multivariate flag (A/B test variant):**

```tsx
import { useStringFlag } from "@tombstone/react";

function HeroBanner() {
  // Returns "control", "headline-a", "headline-b", or the safe default
  const variant = useStringFlag("hero-headline-test", "control");

  const headlines = {
    control: "Ship faster with feature flags",
    "headline-a": "Zero-downtime deploys, every time",
    "headline-b": "From deploy to release — you decide when",
  };

  return <h1>{headlines[variant] ?? headlines.control}</h1>;
}
```

---

### Testing with Feature Flags

Never connect your unit tests to a real flag-api. Tests must be deterministic — a flag value that changes in production should not break your CI pipeline.

Use `TombstoneTestClient` instead:

```typescript
import { TombstoneTestClient } from "@tombstone/core/testing";
import { renderCheckout } from "./checkout";

describe("Checkout rendering", () => {
  let testClient: TombstoneTestClient;

  beforeEach(() => {
    testClient = new TombstoneTestClient();
    // Inject the test client into your module however your DI works
    setTombstoneClient(testClient);
  });

  it("renders new checkout when flag is on", async () => {
    testClient.setFlag("checkout-v2", true);

    const result = renderCheckout(mockCart);

    expect(result).toContain("NewCheckout");
  });

  it("renders old checkout when flag is off", async () => {
    testClient.setFlag("checkout-v2", false);

    const result = renderCheckout(mockCart);

    expect(result).toContain("OldCheckout");
  });

  it("falls back to safe default when flag does not exist", async () => {
    // Do not set the flag — test client returns the safe default (false)
    const result = renderCheckout(mockCart);

    expect(result).toContain("OldCheckout");
  });
});
```

The `TombstoneTestClient` implements the same interface as the real client. It never makes network calls. It returns exactly what you tell it to. Every test that involves a flag should have explicit test cases for both `true` and `false` variants plus the missing-flag (safe default) case.

---

## 9. Understanding Blast Radius

The Blast Radius is Tombstone's risk score for each flag. The evaluator service computes it continuously based on rollout percentage, flag type, environment, and historical incident correlation.

There are four levels:

### BLOCKED

The evaluator has determined this change cannot proceed without an explicit override. This typically means:
- The flag has caused or been correlated with a recent production incident
- The circuit breaker has fired for this flag
- An OPA policy rule is blocking the change

**What to do:** Read the reason shown in the UI. If it is a circuit breaker, investigate the incident first. If it is a policy rule, follow the override procedure or get an admin to review.

### HIGH

The flag touches a critical path — payments, authentication, user data writes, or another high-impact surface. Tombstone will require four-eyes approval for changes at this level.

**What to do:** This is expected and correct behavior for payment or auth flags. The approval workflow is protecting you. Submit a change request and have a teammate approve it.

A payment flag will almost always be HIGH blast radius. That is not a warning — it is correct.

### MEDIUM

Moderate user or revenue exposure. The flag affects a meaningful surface but not a critical one.

**What to do:** Proceed with normal caution. Consider using the canary rollout pattern rather than going straight to 100%. Monitor for 30 minutes after each ramp-up.

### LOW

Limited surface area. Few users affected, low revenue exposure, or the flag controls something that fails safely (like a UI tweak or a logging change).

**What to do:** You can proceed with standard review. Still monitor — blast radius is an estimate, not a guarantee.

---

**When to ignore blast radius:** Blast radius is a tool, not a mandate (except for BLOCKED). A flag that controls a rarely-used admin UI might show MEDIUM because it touches an authenticated endpoint — but the real risk is LOW. Use judgment.

**When to take it seriously:** Any time blast radius is HIGH or BLOCKED on a payment, auth, or data-write path. These are the flags that cause incidents. The evaluator has more context than your intuition at 2pm on a Tuesday.

---

## 10. Troubleshooting Common Issues

### Flag is enabled but my code still sees false

Work through this checklist in order:

**1. Are you in the right environment?**

Your SDK is configured with an `environment` parameter. Check that it matches the environment where you enabled the flag. A flag enabled in `production` is invisible to an SDK pointed at `staging`.

```typescript
// Is this "development", "staging", or "production"?
const client = new TombstoneClient({
  environment: "development",  // <-- check this
});
```

**2. Is the flag key spelled correctly?**

Flag keys are case-sensitive. `checkout-v2` and `Checkout-V2` are different keys. Copy the key directly from the dashboard with `Cmd+K`.

**3. Is the SDK initialized before you call isEnabled?**

The SDK must `await client.initialize()` before any `getBooleanValue` calls. If you call it before initialization completes, you get the safe default (false).

```typescript
// Wrong — calling before initialize
const client = new TombstoneClient({ ... });
const enabled = await client.getBooleanValue("my-flag", false); // returns false

// Right
const client = new TombstoneClient({ ... });
await client.initialize(); // wait for this
const enabled = await client.getBooleanValue("my-flag", false); // returns real value
```

**4. Is the SDK token valid?**

The token must match the environment. Development flags use the development token. Production flags require the production token.

Check that your `TOMBSTONE_SDK_TOKEN` environment variable is set and has not expired.

**5. Is flag-api reachable from your process?**

```bash
curl -s \
  -H "Authorization: Bearer sdk-dev-token-change-in-prod" \
  "http://localhost:8081/api/v1/flags/my-flag/evaluate?environment=development"
```

If this returns an error, flag-api is not reachable. Check `scripts/dev-local.sh status`.

---

### Dashboard shows "Offline" for Governance, SLO, or Experiments sections

Some dashboard features require optional services or warehouse connectors that are not enabled by default. They are controlled by `VITE_ENABLE_*` environment variables in `infra/.env`.

**To enable them:**

```bash
# Open the environment file
open infra/.env
```

Find the relevant variable and set it to `true`:

```
VITE_ENABLE_GOVERNANCE=true
VITE_ENABLE_EXPERIMENTS=true
VITE_ENABLE_SLO=true
```

Then restart the dashboard:

```bash
make down && make dev
```

If you only want to restart the dashboard without rebuilding everything:

```bash
scripts/dev-local.sh restart dashboard
```

---

### Change request stuck in pending

A change request needs someone with the `approver` role to approve it. If it has been pending for more than a day, the most likely cause is that nobody with approver role has been notified.

**How to check roles:**

Open Settings → Team Members. Look for users with the `approver` role assigned.

If there are no approvers, an admin needs to assign the role: click a user → Edit Roles → add `approver`.

If Slack is connected, approvers receive notifications automatically. If Slack is not connected, approvers need to check `/approvals` manually — consider connecting Slack in the Marketplace view.

---

### Intelligence service is slow (first run)

The first time the intelligence service starts, it downloads and bakes the `BAAI/bge-m3` embedding model into its image. This is approximately 2.3 GB and can take 5–15 minutes on a first run depending on your internet connection.

**How to tell this is what is happening:**

```bash
scripts/dev-local.sh logs intelligence
```

You should see log lines like:
```
Downloading model: BAAI/bge-m3 ...
  Progress: 45%
```

Once the model is cached, every subsequent startup is instant. The cache persists in the Docker volume — `make down` preserves it. Only `make down && docker volume prune` would remove the cache.

You do not need to wait for intelligence to use the rest of Tombstone. flag-api, gateway, and the dashboard all work independently.

---

## 11. Keyboard Shortcuts

| Shortcut | Action |
|----------|--------|
| `Cmd+K` (Mac) / `Ctrl+K` (Win/Linux) | Open command palette from any view |
| `Cmd+K` then type flag key | Jump directly to flag detail |
| `Cmd+K` then type `new flag` | Open the Create Flag form |
| `Esc` | Close any modal, drawer, or palette |
| `C` | Switch density to Compact view (more flags visible on screen) |
| `N` | Switch density to Normal view (default) |
| `S` | Switch density to Spacious view (easier reading) |

---

## 12. API Quick Reference

All requests to flag-api require:

```
Authorization: Bearer sdk-dev-token-change-in-prod
```

Replace the token with your actual SDK token in non-development environments. Never hardcode production tokens in code — use environment variables.

Base URL: `http://localhost:8081`

---

### List all flags

```bash
curl -s \
  -H "Authorization: Bearer sdk-dev-token-change-in-prod" \
  "http://localhost:8081/api/v1/flags" \
  | jq .
```

Returns an array of all flag objects. Supports query params: `?environment=production`, `?type=BOOLEAN`, `?status=active`.

---

### Create a flag

```bash
curl -s -X POST \
  -H "Authorization: Bearer sdk-dev-token-change-in-prod" \
  -H "Content-Type: application/json" \
  -d '{
    "key": "my-new-flag",
    "name": "My New Flag",
    "description": "Created via API",
    "type": "BOOLEAN",
    "safe_default": false
  }' \
  "http://localhost:8081/api/v1/flags" \
  | jq .
```

**Required fields:** `key`, `name`, `type`
**Optional:** `description`, `safe_default` (defaults to `false`)

---

### Toggle a flag and set rollout percentage

```bash
curl -s -X PATCH \
  -H "Authorization: Bearer sdk-dev-token-change-in-prod" \
  -H "Content-Type: application/json" \
  -d '{
    "enabled": true,
    "rollout_percentage": 50
  }' \
  "http://localhost:8081/api/v1/flags/my-new-flag/environments/production" \
  | jq .
```

**Fields:** `enabled` (boolean), `rollout_percentage` (0–100)

You can send either field independently. PATCH is partial update — omitting `enabled` does not disable the flag.

---

### Get all flag states for an environment

Useful for server-side rendering — fetch all flags once at request time instead of making a separate call per flag.

```bash
curl -s \
  -H "Authorization: Bearer sdk-dev-token-change-in-prod" \
  "http://localhost:8081/api/v1/environments/snapshot?environment=production" \
  | jq .
```

Returns a compact map of every flag key to its current value for the requested environment.

```json
{
  "checkout-v2": true,
  "payment-processor-v3": false,
  "new-nav": true,
  "beta-dashboard": false
}
```

---

### Get audit trail for a flag

```bash
curl -s \
  -H "Authorization: Bearer sdk-dev-token-change-in-prod" \
  "http://localhost:8081/api/v1/audit?flag_key=my-new-flag" \
  | jq .
```

Returns the full audit log for the specified flag key in descending timestamp order. Supports `?limit=50` to paginate.

---

### Evaluate a flag directly

```bash
curl -s \
  -H "Authorization: Bearer sdk-dev-token-change-in-prod" \
  "http://localhost:8081/api/v1/flags/my-new-flag/evaluate?environment=development&context[userId]=user-12345" \
  | jq .
```

Returns the evaluated value for the given user context:

```json
{
  "value": true,
  "reason": "TARGETING_MATCH",
  "flag_key": "my-new-flag",
  "environment": "development",
  "variant": null
}
```

Possible `reason` values: `FULL_ROLLOUT`, `PARTIAL_ROLLOUT`, `TARGETING_MATCH`, `DISABLED`, `PREREQUISITE_NOT_MET`, `SAFE_DEFAULT`.

---

### Check blast radius

```bash
curl -s \
  "http://localhost:8082/api/v1/blast-radius/my-new-flag" \
  | jq .
```

Note: blast radius is served by the evaluator on port `8082`, not flag-api on `8081`.

---

## 13. Glossary Reference

Quick one-line definitions for terms you will see throughout Tombstone. Full definitions are in [docs/GLOSSARY.md](./GLOSSARY.md).

| Term | What it means |
|------|--------------|
| **Audit Log** | Append-only, tamper-proof record of every flag change — who, what, when |
| **Blast Radius** | Risk score (BLOCKED / HIGH / MEDIUM / LOW) for a flag based on its impact surface |
| **Break-Glass Token** | Emergency SDK token that bypasses approval workflow; scoped and time-limited |
| **Circuit Breaker** | Auto-rollback mechanism — fires when a flag causes >5% errors on 100 requests |
| **Change Request** | A pending flag modification that needs a second human to approve before applying |
| **Environment** | Isolation boundary — development, staging, production each have independent flag states |
| **Flag Key** | The permanent, immutable identifier for a flag — cannot be reused after archiving |
| **Kill Switch** | A flag you disable (not enable) to turn off a broken feature instantly |
| **Prerequisites** | Flags that must be on before this flag can turn on — enforces feature dependencies |
| **Rollout Percentage** | The fraction of users (0–100%) who receive the enabled value; sticky per user |
| **Safe Default** | The value returned when flag-api is unreachable — always configure this carefully |
| **SDK Token** | Authentication key for the SDKs and API callers; one per environment |
| **Tombstoning** | Permanent archival of a flag key — blocks reuse forever (Knight Capital prevention) |
| **Variation** | Named value for multivariate flags — e.g. `blue`, `green`, `control` |

For the full definition of any term — including the technical detail of how Merkle linking works, the exact circuit breaker thresholds, and the OPA RBAC policy model — see [docs/GLOSSARY.md](./GLOSSARY.md).

---

*Built with Tombstone v2.2.0. Last updated 2026-06-27.*
