# services/intelligence/app/search/embedding_model_bedrock.py
from __future__ import annotations

import asyncio
import base64
import json
import logging

logger = logging.getLogger(__name__)

_MODEL_ID = "amazon.titan-embed-text-v2:0"
_DIMENSIONS = 1024


def decode_secret(value: str | None) -> str:
    """
    Decode a base64-encoded secret; return it unchanged if it isn't base64.
    Uses a round-trip equality check (re-encode the decoded value and compare) —
    many raw secrets are coincidentally valid base64 so a plain "decodes ok" test
    is too loose. Raw AKIA… keys are not valid base64 of themselves, so they fall
    through unchanged. Empty/None → "".

    Mirrors Anvilry's TypeScript decodeSecret() in src/lib/llm.ts exactly.
    """
    if not value:
        return ""
    try:
        decoded = base64.b64decode(value).decode("utf-8")
        # Round-trip: if re-encoding matches the original it was valid base64
        if base64.b64encode(decoded.encode("utf-8")).decode() == value:
            return decoded
    except Exception:
        pass
    return value


class BedrockEmbeddingModel:
    """
    Calls AWS Bedrock Titan Text Embeddings V2 via boto3.
    Same credentials as Anvilry: BEDROCK_ACCESS_KEY_ID / BEDROCK_SECRET_ACCESS_KEY / BEDROCK_REGION.
    Cost: ~$0.02 / 1M tokens. At flag-update rates (~100 flags × 50 tokens) ≈ $0.0001/month.
    No free tier — every call is billed, but volume is negligible.
    """

    def __init__(
        self,
        access_key_id: str,
        secret_access_key: str,
        region: str,
        model_id: str = _MODEL_ID,
    ) -> None:
        # decode_secret() handles both raw and base64-encoded values (same as Anvilry)
        self._access_key_id = decode_secret(access_key_id)
        self._secret_access_key = decode_secret(secret_access_key)
        self._region = region
        self._model_id = model_id
        self._client = None

    async def initialize(self) -> None:
        import boto3
        self._client = boto3.client(
            "bedrock-runtime",
            aws_access_key_id=self._access_key_id,
            aws_secret_access_key=self._secret_access_key,
            region_name=self._region,
        )
        logger.info("BedrockEmbeddingModel: initialized with model %s in %s", self._model_id, self._region)

    async def embed(self, texts: list[str]) -> list[list[float]]:
        """
        Returns 1024-dim vectors for each text.
        Calls invoke_model once per text (Titan V2 embeds one text per call).
        Runs in executor to avoid blocking the event loop.
        """
        if self._client is None:
            raise RuntimeError("BedrockEmbeddingModel not initialized — call await initialize() first")

        loop = asyncio.get_running_loop()
        results: list[list[float]] = []

        for text in texts:
            body = json.dumps({
                "inputText": text,
                "dimensions": _DIMENSIONS,
                "normalize": True,
            })
            try:
                response = await loop.run_in_executor(
                    None,
                    lambda b=body: self._client.invoke_model(
                        modelId=self._model_id,
                        body=b,
                        contentType="application/json",
                        accept="application/json",
                    ),
                )
                payload = json.loads(response["body"].read())
                results.append(payload["embedding"])
            except Exception as exc:
                logger.error("BedrockEmbeddingModel: invoke_model failed for text snippet %r: %s", text[:50], exc)
                results.append([0.0] * _DIMENSIONS)  # zero vector on failure — won't break pgvector

        return results

    async def close(self) -> None:
        self._client = None
