from fastapi import APIRouter
from pydantic import BaseModel

from app.cleanup.pr_generator import CleanupPRGenerator

router = APIRouter(prefix="/api/v1/cleanup", tags=["cleanup"])


class GeneratePRRequest(BaseModel):
    flag_key: str
    flag_name: str
    flag_description: str
    owner_id: str
    days_at_100_pct: float
    stale_score: float
    evaluation_count: int = 0


class GeneratePRResponse(BaseModel):
    branch_name: str
    pr_title: str
    pr_body: str
    commit_message: str
    confidence: str


@router.post("/generate-pr", response_model=GeneratePRResponse)
async def generate_cleanup_pr(body: GeneratePRRequest) -> GeneratePRResponse:
    generator = CleanupPRGenerator()
    spec = await generator.generate(
        flag_key=body.flag_key,
        flag_name=body.flag_name,
        flag_description=body.flag_description,
        owner_id=body.owner_id,
        days_at_100_pct=body.days_at_100_pct,
        stale_score=body.stale_score,
        evaluation_count=body.evaluation_count,
    )
    return GeneratePRResponse(
        branch_name=spec.branch_name,
        pr_title=spec.pr_title,
        pr_body=spec.pr_body,
        commit_message=spec.commit_message,
        confidence=spec.confidence,
    )
