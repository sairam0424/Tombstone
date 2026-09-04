"""
Argos-inspired 3-agent LLM pipeline for autonomous anomaly rule generation.
Based on: Argos arXiv 2501.14170 — Detection Agent -> Repair Agent -> Review Agent

Pipeline:
  1. Detection Agent  — analyzes flag telemetry, describes anomaly pattern
  2. Repair Agent     — generates Python detection function, fixes syntax errors
  3. Review Agent     — validates rule on held-out 20% of history data

Generated rules are stored as pending-approval signals, NEVER auto-activated.
Requires ANTHROPIC_API_KEY. Returns graceful failure dict when absent.
"""
from __future__ import annotations

import ast
import json
import logging
import os
from dataclasses import dataclass
from datetime import datetime, timezone
from pathlib import Path

import httpx
import numpy as np

logger = logging.getLogger(__name__)

ANTHROPIC_API_URL = "https://api.anthropic.com/v1/messages"
MODEL = "claude-haiku-4-5-20251001"
MAX_REPAIR_ROUNDS = 3


@dataclass
class RuleResult:
    flag_key: str
    rule_code: str = ""
    description: str = ""
    precision: float = 0.0
    recall: float = 0.0
    approved: bool = False
    error: str = ""
    signal_path: str = ""


async def _call_llm(prompt: str, api_key: str, max_tokens: int = 512) -> str:
    """Single LLM call via raw httpx (matches existing pattern in experiments/routes.py)."""
    async with httpx.AsyncClient(timeout=30.0) as client:
        response = await client.post(
            ANTHROPIC_API_URL,
            headers={
                "x-api-key": api_key,
                "anthropic-version": "2023-06-01",
                "content-type": "application/json",
            },
            json={
                "model": MODEL,
                "max_tokens": max_tokens,
                "messages": [{"role": "user", "content": prompt}],
            },
        )
        response.raise_for_status()
        return response.json()["content"][0]["text"].strip()


def _validate_rule_syntax(code: str) -> tuple[bool, str]:
    """Validate generated Python rule code using ast.parse()."""
    try:
        tree = ast.parse(code)
        # Check for forbidden constructs (imports, file access, subprocess)
        for node in ast.walk(tree):
            if isinstance(node, (ast.Import, ast.ImportFrom)):
                return False, "imports not allowed in rule code"
            if isinstance(node, ast.Call):
                if isinstance(node.func, ast.Name) and node.func.id in (
                    "open", "exec", "eval", "__import__",
                ):
                    return False, f"forbidden built-in: {node.func.id}"
        return True, ""
    except SyntaxError as e:
        return False, str(e)


def _held_out_split(
    error_rates: list[float],
    holdout_pct: float = 0.2,
) -> tuple[list[float], list[float]]:
    """Split error_rates into training (80%) and held-out (20%) sets."""
    n = len(error_rates)
    split = max(1, int(n * (1 - holdout_pct)))
    return error_rates[:split], error_rates[split:]


async def generate_rule(
    flag_key: str,
    error_rates: list[float],
    signals_dir: str,
) -> RuleResult:
    """
    Run the 3-agent Argos pipeline for flag_key.

    error_rates: list of recent error rate observations (from AnomalyDetector metrics).
    signals_dir: path to write the pending-approval signal file.
    """
    result = RuleResult(flag_key=flag_key)
    api_key = os.environ.get("ANTHROPIC_API_KEY", "")

    if not api_key:
        result.error = "ANTHROPIC_API_KEY not set — rule generation unavailable"
        logger.warning("rule_generator: %s", result.error)
        return result

    if len(error_rates) < 30:
        result.error = (
            f"insufficient data: {len(error_rates)} observations (need >=30)"
        )
        return result

    train_rates, holdout_rates = _held_out_split(error_rates)
    train_arr = np.array(train_rates)

    # ── Agent 1: Detection Agent ─────────────────────────────────────────────
    detection_prompt = (
        f"You are analyzing anomaly detection data for a feature flag.\n\n"
        f"Flag key: {flag_key}\n"
        f"Training error rates (last {len(train_rates)} observations): "
        f"{json.dumps([round(r, 4) for r in train_rates[-50:]])}\n\n"
        f"Statistics:\n"
        f"- Mean: {float(np.mean(train_arr)):.4f}\n"
        f"- Std: {float(np.std(train_arr)):.4f}\n"
        f"- Max: {float(np.max(train_arr)):.4f}\n"
        f"- P95: {float(np.percentile(train_arr, 95)):.4f}\n"
        f"- P99: {float(np.percentile(train_arr, 99)):.4f}\n\n"
        "Describe in 2-3 sentences: what anomaly pattern exists in this data?\n"
        "What threshold or pattern would best detect anomalies?\n"
        "Be specific about numbers."
    )

    try:
        description = await _call_llm(detection_prompt, api_key, max_tokens=256)
        result.description = description
        logger.info("rule_generator: detection agent completed for %s", flag_key)
    except Exception as exc:
        result.error = f"detection agent failed: {exc}"
        return result

    # ── Agent 2: Repair Agent (generate + fix syntax loop) ────────────────────
    repair_prompt = (
        f"You are generating a Python anomaly detection function.\n\n"
        f"Flag: {flag_key}\n"
        f"Pattern description: {description}\n\n"
        "Write ONLY a Python function called `detect_anomaly(error_rate: float, history: list) -> bool`.\n"
        "Rules:\n"
        "- No imports\n"
        "- No file access\n"
        "- Uses only: math operations, list operations, len(), sum(), max(), min()\n"
        "- Returns True if error_rate is anomalous given history\n"
        "- Must be <=20 lines\n\n"
        "Return ONLY the function code, no explanation."
    )

    rule_code = ""
    for attempt in range(MAX_REPAIR_ROUNDS):
        try:
            generated = await _call_llm(repair_prompt, api_key, max_tokens=256)
            # Strip markdown code fences if present
            generated = generated.strip()
            if generated.startswith("```"):
                lines = generated.split("\n")
                inner = lines[1:-1] if lines[-1].strip() == "```" else lines[1:]
                generated = "\n".join(inner)

            valid, err = _validate_rule_syntax(generated)
            if valid:
                rule_code = generated
                break
            else:
                repair_prompt = (
                    f"The previous function had a syntax error: {err}\n"
                    "Fix it. Return ONLY valid Python function code, no imports, no explanation.\n"
                    f"Previous attempt:\n{generated}"
                )
        except Exception as exc:
            logger.warning(
                "rule_generator: repair attempt %d failed: %s", attempt + 1, exc
            )

    if not rule_code:
        result.error = (
            f"repair agent: could not generate valid code after {MAX_REPAIR_ROUNDS} attempts"
        )
        return result

    result.rule_code = rule_code
    logger.info("rule_generator: repair agent produced valid code for %s", flag_key)

    # ── Agent 3: Review Agent ────────────────────────────────────────────────
    review_prompt = (
        f"You are reviewing an anomaly detection rule.\n\n"
        f"Rule code:\n{rule_code}\n\n"
        f"Held-out test data ({len(holdout_rates)} observations):\n"
        f"Error rates: {json.dumps([round(r, 4) for r in holdout_rates])}\n"
        f"Mean training error rate: {float(np.mean(train_arr)):.4f}\n"
        f"P95 training error rate: {float(np.percentile(train_arr, 95)):.4f}\n\n"
        "Would this rule correctly identify the anomalous observations "
        "(values significantly above mean+2*std)?\n"
        "Estimate: precision (fraction of flagged that are real anomalies) "
        "and recall (fraction of anomalies caught).\n"
        'Respond in JSON: {"precision": 0.0, "recall": 0.0, "assessment": "brief judgment"}'
    )

    try:
        review_raw = await _call_llm(review_prompt, api_key, max_tokens=256)
        review_raw = review_raw.strip()
        if review_raw.startswith("```"):
            lines = review_raw.split("\n")
            review_raw = "\n".join(lines[1:-1])
        review_data = json.loads(review_raw)
        result.precision = float(review_data.get("precision", 0))
        result.recall = float(review_data.get("recall", 0))
        logger.info(
            "rule_generator: review agent: precision=%.2f recall=%.2f",
            result.precision,
            result.recall,
        )
    except Exception as exc:
        logger.warning("rule_generator: review agent failed: %s", exc)
        result.precision = 0.0
        result.recall = 0.0

    # ── Write pending-approval signal ────────────────────────────────────────
    date_str = datetime.now(timezone.utc).strftime("%Y-%m-%d")
    signal_name = f"rule-candidate-{flag_key}-{date_str}.md"
    signal_path = Path(signals_dir) / signal_name
    Path(signals_dir).mkdir(parents=True, exist_ok=True)

    signal_content = f"""---
kind: signal
category: idea
frequency: on-demand
sources: [argos-llm-pipeline]
domain: [flag-cleanup, incident-response]
status: pending-approval
flag_key: {flag_key}
precision: {result.precision:.2f}
recall: {result.recall:.2f}
---

# Generated anomaly rule for `{flag_key}`

**Status:** PENDING OWNER APPROVAL — not yet active

**Description:** {description}

**Review:** precision={result.precision:.2f} recall={result.recall:.2f}

## Generated Rule Code

```python
{rule_code}
```

## Activation

To activate this rule, set `status: approved` in this file's frontmatter
and restart the intelligence service. The rule will be loaded at startup.

## Timeline
{date_str} | Generated by Argos 3-agent pipeline. Pending approval.
"""

    signal_path.write_text(signal_content)
    result.signal_path = str(signal_path)
    logger.info("rule_generator: signal written to %s", signal_path)

    return result
