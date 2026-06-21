import os
from dataclasses import dataclass


@dataclass
class CleanupPRSpec:
    flag_key: str
    branch_name: str
    pr_title: str
    pr_body: str
    commit_message: str
    confidence: str


class CleanupPRGenerator:
    def __init__(self, anthropic_api_key: str = "") -> None:
        self._api_key = anthropic_api_key or os.environ.get("ANTHROPIC_API_KEY", "")

    async def generate(
        self,
        flag_key: str,
        flag_name: str,
        flag_description: str,
        owner_id: str,
        days_at_100_pct: float,
        stale_score: float,
        evaluation_count: int = 0,
    ) -> CleanupPRSpec:
        branch = f"chore/flags/remove-{flag_key.replace('.', '-')}"
        pr_title = f"chore(flags): remove {flag_key} — stale {int(days_at_100_pct)}d at 100%"
        commit_message = (
            f"chore(flags): tombstone {flag_key}\n\n"
            f"Flag has been at 100% rollout for {int(days_at_100_pct)} days.\n"
            f"Stale score: {stale_score:.2f}. Safe to remove."
        )

        if stale_score > 0.8:
            confidence = "HIGH"
        elif stale_score > 0.5:
            confidence = "MEDIUM"
        else:
            confidence = "LOW"

        pr_body = await self._generate_pr_body(
            flag_key=flag_key,
            flag_name=flag_name,
            flag_description=flag_description,
            owner_id=owner_id,
            days_at_100_pct=days_at_100_pct,
            stale_score=stale_score,
            evaluation_count=evaluation_count,
            confidence=confidence,
        )

        return CleanupPRSpec(
            flag_key=flag_key,
            branch_name=branch,
            pr_title=pr_title,
            pr_body=pr_body,
            commit_message=commit_message,
            confidence=confidence,
        )

    async def _generate_pr_body(
        self,
        flag_key: str,
        flag_name: str,
        flag_description: str,
        owner_id: str,
        days_at_100_pct: float,
        stale_score: float,
        evaluation_count: int,
        confidence: str,
    ) -> str:
        if self._api_key:
            return await self._generate_with_anthropic(
                flag_key=flag_key,
                flag_name=flag_name,
                flag_description=flag_description,
                owner_id=owner_id,
                days_at_100_pct=days_at_100_pct,
                stale_score=stale_score,
                evaluation_count=evaluation_count,
                confidence=confidence,
            )
        return self._template_pr_body(
            flag_key=flag_key,
            flag_name=flag_name,
            flag_description=flag_description,
            owner_id=owner_id,
            days_at_100_pct=days_at_100_pct,
            stale_score=stale_score,
            evaluation_count=evaluation_count,
            confidence=confidence,
        )

    async def _generate_with_anthropic(
        self,
        flag_key: str,
        flag_name: str,
        flag_description: str,
        owner_id: str,
        days_at_100_pct: float,
        stale_score: float,
        evaluation_count: int,
        confidence: str,
    ) -> str:
        try:
            import anthropic

            client = anthropic.Anthropic(api_key=self._api_key)
            prompt = (
                f"Generate a concise GitHub pull request body for removing a stale feature flag.\n\n"
                f"Flag key: {flag_key}\n"
                f"Flag name: {flag_name}\n"
                f"Description: {flag_description}\n"
                f"Owner: {owner_id}\n"
                f"Days at 100% rollout: {int(days_at_100_pct)}\n"
                f"Stale score: {stale_score:.2f} (confidence: {confidence})\n"
                f"Total evaluations: {evaluation_count}\n\n"
                "Include: ## Summary, ## Why safe to remove, ## Cleanup checklist (with markdown checkboxes). "
                "Keep it under 300 words and be direct."
            )
            message = client.messages.create(
                model="claude-haiku-4-5-20251001",
                max_tokens=512,
                messages=[{"role": "user", "content": prompt}],
            )
            return message.content[0].text
        except Exception:
            return self._template_pr_body(
                flag_key=flag_key,
                flag_name=flag_name,
                flag_description=flag_description,
                owner_id=owner_id,
                days_at_100_pct=days_at_100_pct,
                stale_score=stale_score,
                evaluation_count=evaluation_count,
                confidence=confidence,
            )

    def _template_pr_body(
        self,
        flag_key: str,
        flag_name: str,
        flag_description: str,
        owner_id: str,
        days_at_100_pct: float,
        stale_score: float,
        evaluation_count: int,
        confidence: str,
    ) -> str:
        return (
            f"## Summary\n\n"
            f"Removes stale feature flag `{flag_key}` ({flag_name}).\n\n"
            f"> {flag_description}\n\n"
            f"**Owner:** {owner_id}  \n"
            f"**Days at 100% rollout:** {int(days_at_100_pct)}  \n"
            f"**Stale score:** {stale_score:.2f}  \n"
            f"**Confidence:** {confidence}  \n"
            f"**Total evaluations:** {evaluation_count}\n\n"
            f"## Why safe to remove\n\n"
            f"- Flag has been at 100% rollout for {int(days_at_100_pct)} days with no rollback.\n"
            f"- Tombstone stale-detection score is `{stale_score:.2f}` (threshold: 0.5).\n"
            f"- All users are already receiving the flagged behavior — the guard code is dead weight.\n\n"
            f"## Cleanup checklist\n\n"
            f"- [ ] Delete flag evaluation calls referencing `{flag_key}` from application code\n"
            f"- [ ] Remove any A/B test instrumentation tied to this flag\n"
            f"- [ ] Archive flag record in Tombstone dashboard (tombstone)\n"
            f"- [ ] Confirm no SDK clients reference `{flag_key}` after merge\n"
            f"- [ ] Update feature documentation to reflect permanent enablement\n"
        )
