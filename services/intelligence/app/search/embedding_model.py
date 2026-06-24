# services/intelligence/app/search/embedding_model.py
from __future__ import annotations

import asyncio
import logging
from typing import Protocol, runtime_checkable

logger = logging.getLogger(__name__)


@runtime_checkable
class EmbeddingModel(Protocol):
    async def initialize(self) -> None: ...
    async def embed(self, texts: list[str]) -> list[list[float]]: ...
    async def close(self) -> None: ...


class LocalEmbeddingModel:
    """Wraps sentence-transformers SentenceTransformer — original behaviour."""

    def __init__(self, model_name: str = "BAAI/bge-m3") -> None:
        self._model_name = model_name
        self._model = None

    async def initialize(self) -> None:
        loop = asyncio.get_running_loop()
        try:
            from sentence_transformers import SentenceTransformer
            self._model = await loop.run_in_executor(
                None, lambda: SentenceTransformer(self._model_name)
            )
            logger.info("LocalEmbeddingModel: loaded %s", self._model_name)
        except Exception as exc:
            logger.warning("LocalEmbeddingModel: failed to load model (%s) — embed() will return []", exc)
            self._model = None

    async def embed(self, texts: list[str]) -> list[list[float]]:
        if self._model is None:
            return [[] for _ in texts]
        loop = asyncio.get_running_loop()
        vectors = await loop.run_in_executor(None, lambda: self._model.encode(texts))
        return [v.tolist() if hasattr(v, "tolist") else list(v) for v in vectors]

    async def close(self) -> None:
        self._model = None


def create_embedding_model(backend: str, **kwargs) -> EmbeddingModel:
    """
    Factory. Pass backend='local' or backend='bedrock'.
    kwargs for bedrock: access_key_id, secret_access_key, region
    """
    if backend == "local":
        model_name = kwargs.get("model_name", "BAAI/bge-m3")
        return LocalEmbeddingModel(model_name)
    if backend == "bedrock":
        from app.search.embedding_model_bedrock import BedrockEmbeddingModel
        return BedrockEmbeddingModel(
            access_key_id=kwargs["access_key_id"],
            secret_access_key=kwargs["secret_access_key"],
            region=kwargs["region"],
        )
    raise ValueError(f"Unknown embedding backend: {backend!r}. Choose 'local' or 'bedrock'.")
