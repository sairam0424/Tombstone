# services/intelligence/tests/test_embedding_model.py
from __future__ import annotations

import pytest
from unittest.mock import MagicMock, patch

from app.search.embedding_model import LocalEmbeddingModel, create_embedding_model


@pytest.mark.asyncio
async def test_local_embedding_model_embed_returns_1024_dims():
    """LocalEmbeddingModel.embed() returns list of 1024-dim vectors."""
    mock_model = MagicMock()
    mock_model.encode.return_value = [[0.1] * 1024, [0.2] * 1024]

    with patch("app.search.embedding_model.SentenceTransformer", return_value=mock_model, create=True):
        model = LocalEmbeddingModel("BAAI/bge-m3")
        model._model = mock_model  # bypass initialize()
        result = await model.embed(["flag description", "another flag"])

    assert len(result) == 2
    assert len(result[0]) == 1024


def test_create_embedding_model_returns_local_by_default():
    model = create_embedding_model("local")
    assert isinstance(model, LocalEmbeddingModel)


def test_create_embedding_model_unknown_backend_raises():
    with pytest.raises(ValueError, match="Unknown embedding backend"):
        create_embedding_model("unknown_backend")


@pytest.mark.asyncio
async def test_bedrock_embedding_model_embed_returns_1024_dims():
    """BedrockEmbeddingModel.embed() calls invoke_model and returns 1024-dim vectors."""
    import json
    from io import BytesIO

    fake_response_body = json.dumps({
        "embedding": [0.1] * 1024,
        "inputTextTokenCount": 5,
    }).encode()

    mock_client = MagicMock()
    mock_client.invoke_model.return_value = {
        "body": BytesIO(fake_response_body),
        "contentType": "application/json",
    }

    with patch("boto3.client", return_value=mock_client):
        from app.search.embedding_model_bedrock import BedrockEmbeddingModel
        model = BedrockEmbeddingModel(
            access_key_id="fake_key",
            secret_access_key="fake_secret",
            region="us-east-1",
        )
        await model.initialize()
        result = await model.embed(["flag key: checkout-v2 — enables new checkout flow"])

    assert len(result) == 1
    assert len(result[0]) == 1024
    mock_client.invoke_model.assert_called_once()
    call_kwargs = mock_client.invoke_model.call_args[1]
    assert call_kwargs["modelId"] == "amazon.titan-embed-text-v2:0"


@pytest.mark.asyncio
async def test_bedrock_embedding_model_embed_multiple_texts():
    """BedrockEmbeddingModel.embed() calls invoke_model once per text."""
    import json
    from io import BytesIO

    mock_client = MagicMock()
    mock_client.invoke_model.return_value = {
        "body": BytesIO(json.dumps({"embedding": [0.1] * 1024}).encode()),
        "contentType": "application/json",
    }

    with patch("boto3.client", return_value=mock_client):
        from app.search.embedding_model_bedrock import BedrockEmbeddingModel
        model = BedrockEmbeddingModel("k", "s", "us-east-1")
        await model.initialize()
        result = await model.embed(["text one", "text two", "text three"])

    assert len(result) == 3
    assert mock_client.invoke_model.call_count == 3


def test_create_embedding_model_returns_bedrock():
    with patch("app.search.embedding_model_bedrock.BedrockEmbeddingModel") as MockBedrock:
        model = create_embedding_model(
            "bedrock",
            access_key_id="k",
            secret_access_key="s",
            region="us-east-1",
        )
        MockBedrock.assert_called_once_with(
            access_key_id="k",
            secret_access_key="s",
            region="us-east-1",
        )
