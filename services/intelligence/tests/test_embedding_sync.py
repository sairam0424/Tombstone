"""Tests for sync_flag_embedding's handling of a failed embedding."""

from __future__ import annotations

from unittest.mock import AsyncMock, MagicMock

import pytest

from app.search.embedding_model_bedrock import BedrockEmbeddingModel
from app.search.embedding_sync import sync_flag_embedding


@pytest.mark.asyncio
async def test_sync_persists_nothing_when_bedrock_fails_to_embed(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    """Regression: BedrockEmbeddingModel used to answer a failed invoke_model
    with a zero vector. pgvector accepts one, so the UPDATE stored it and the
    flag left the embedding IS NULL backfill queue for good."""
    client = MagicMock()
    client.invoke_model.side_effect = RuntimeError("ThrottlingException")
    model = BedrockEmbeddingModel("k", "s", "us-east-1")
    monkeypatch.setattr(model, "_client", client)  # bypass initialize(): no boto3 needed
    pool = MagicMock()
    pool.execute = AsyncMock()

    await sync_flag_embedding(
        flag_key="checkout-v2",
        name="Checkout v2",
        description="new checkout flow",
        tags=[],
        db_pool=pool,
        model=model,
        project_id="00000000-0000-0000-0000-000000000001",
    )

    pool.execute.assert_not_called()
