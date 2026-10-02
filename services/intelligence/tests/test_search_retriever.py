"""
Tests for FlagSearchRetriever.search() arm selection.

Regression coverage for a real bug mypy surfaced: with no embedding model
configured (the documented lexical-only mode), search() built its dense arm
with `asyncio.coroutine(lambda: [])()`. asyncio.coroutine was removed in
Python 3.11 and this service requires 3.12, so every search without an
embedding model raised AttributeError instead of degrading to lexical +
ILIKE results. The retrieval arms are stubbed; no database is involved.
"""

from __future__ import annotations

from unittest.mock import AsyncMock, MagicMock

import pytest

from app.search.embedding_model import EmbeddingModel
from app.search.retriever import FlagSearchRetriever


def _retriever(embedding_model: EmbeddingModel | None = None) -> FlagSearchRetriever:
    retriever = FlagSearchRetriever(
        db_url="postgresql://unused/unused", embedding_model=embedding_model
    )
    retriever._pool = MagicMock()  # skip asyncpg.create_pool
    return retriever


@pytest.mark.asyncio
async def test_search_without_an_embedding_model_is_lexical_only(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    retriever = _retriever(embedding_model=None)
    vector = AsyncMock(return_value=[{"flag_key": "dense-only"}])
    monkeypatch.setattr(retriever, "_vector_search", vector)
    monkeypatch.setattr(
        retriever,
        "_fulltext_search",
        AsyncMock(return_value=[{"flag_key": "alpha"}, {"flag_key": "beta"}]),
    )
    monkeypatch.setattr(
        retriever, "_ilike_search", AsyncMock(return_value=[{"flag_key": "beta"}])
    )

    results = await retriever.search("checkout", limit=5)

    # beta is ranked by both lexical arms, so RRF puts it ahead of alpha.
    assert [r["flag_key"] for r in results] == ["beta", "alpha"]
    vector.assert_not_called()


@pytest.mark.asyncio
async def test_search_with_an_embedding_model_includes_the_dense_arm(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    retriever = _retriever(embedding_model=MagicMock())
    monkeypatch.setattr(
        retriever,
        "_vector_search",
        AsyncMock(return_value=[{"flag_key": "dense-only"}]),
    )
    monkeypatch.setattr(retriever, "_fulltext_search", AsyncMock(return_value=[]))
    monkeypatch.setattr(retriever, "_ilike_search", AsyncMock(return_value=[]))

    results = await retriever.search("checkout", limit=5)

    assert [r["flag_key"] for r in results] == ["dense-only"]


@pytest.mark.asyncio
async def test_vector_search_without_an_embedding_model_returns_nothing() -> None:
    retriever = _retriever(embedding_model=None)

    assert await retriever._vector_search(MagicMock(), "checkout", 5) == []
