# Fly.io Free Deployment: Bedrock Embeddings + Redis Streams Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the 1.4GB BAAI/bge-m3 ML model with AWS Bedrock Titan V2 embeddings, and replace the aiokafka Kafka consumer with a Redis Streams adapter behind a factory interface — enabling the intelligence service to run in Fly.io's 256MB free tier at $0/month additional cost.

**Architecture:** Two independent changes gated by environment variables. (1) `EMBEDDING_BACKEND=bedrock` swaps `SentenceTransformer.encode()` calls in `retriever.py` and `embedding_sync.py` for boto3 `bedrock-runtime.invoke_model()` — same credentials already used by Anvilry, same 1024-dim pgvector schema, no DDL migration needed. (2) `CONSUMER_BACKEND=redis` swaps the infinite `aiokafka` consumer loop for an `aioredis` `XREADGROUP` loop reading from the `tombstone:stream:{env}` streams that flag-api already writes (Phase 4.1). Both changes are wrapped behind an abstract `EmbeddingModel` protocol and `EventConsumer` ABC so local dev can still use the original implementations.

**Tech Stack:** Python 3.12, boto3 (already available via AWS SDK), `redis.asyncio` (already a dep: `redis[asyncio]>=5.0.0`), asyncpg (existing), FastAPI (existing). No new packages required.

## Global Constraints

- Python 3.12; use `from __future__ import annotations` in new files
- `GOWORK=off` is for Go services only — not relevant here
- New env vars: `EMBEDDING_BACKEND` (`local` | `bedrock`, default `local`), `CONSUMER_BACKEND` (`kafka` | `redis`, default `kafka`)
- Bedrock model ID: `amazon.titan-embed-text-v2:0` — 1024 dims, 8192-token context
- Bedrock credentials: reuse `BEDROCK_ACCESS_KEY_ID`, `BEDROCK_SECRET_ACCESS_KEY`, `BEDROCK_REGION` (same as Anvilry, base64-OR-raw — the `decodeSecret()` pattern is in Anvilry, NOT in Tombstone; just pass them raw to boto3)
- Redis stream key format: `tombstone:stream:{environment}` — matches Phase 4.1 flag-api output exactly
- Consumer group name for intelligence: `intelligence-worker` — distinct from gateway's `gateway-workers`
- `XREADGROUP` block timeout: 1000ms; COUNT: 10 per read
- All new files go under `services/intelligence/app/`
- Run tests from `services/intelligence/`: `cd services/intelligence && python -m pytest tests/ -v`
- All new tests in `services/intelligence/tests/`

---

## File Map

| File | Action | Purpose |
|------|--------|---------|
| `app/search/embedding_model.py` | **Create** | `EmbeddingModel` protocol + `LocalEmbeddingModel` + `BedrockEmbeddingModel` + `create_embedding_model()` factory |
| `app/search/retriever.py` | **Modify** | Replace `SentenceTransformer` with `EmbeddingModel` protocol |
| `app/search/embedding_sync.py` | **Modify** | Replace `SentenceTransformer` with `EmbeddingModel` protocol |
| `app/kafka/consumer.py` | **Modify** | Add `EventConsumer` ABC + `KafkaEventConsumer` wrapper + `RedisStreamsEventConsumer` + `create_consumer()` factory |
| `app/main.py` | **Modify** | Pass `CONSUMER_BACKEND` and `EMBEDDING_BACKEND` to factories at startup |
| `tests/test_embedding_model.py` | **Create** | Unit tests for both embedding backends |
| `tests/test_consumer_factory.py` | **Create** | Unit tests for consumer factory + Redis Streams consumer |

---

## Task 1: `EmbeddingModel` Protocol + Local Backend

**Files:**
- Create: `services/intelligence/app/search/embedding_model.py`
- Create: `services/intelligence/tests/test_embedding_model.py`

**Interfaces:**
- Produces: `EmbeddingModel` protocol with `async def embed(self, texts: list[str]) -> list[list[float]]` and `async def initialize(self) -> None` and `async def close(self) -> None`
- Produces: `LocalEmbeddingModel(model_name: str)` — wraps existing SentenceTransformer
- Produces: `create_embedding_model(backend: str, **kwargs) -> EmbeddingModel` factory

- [ ] **Step 1: Write failing test for the protocol**

```python
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

    with patch("app.search.embedding_model.SentenceTransformer", return_value=mock_model):
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
```

- [ ] **Step 2: Run test to verify it fails**

```bash
cd /Users/sairamugge/Desktop/Not-Humans-World/Tombstone/services/intelligence
python -m pytest tests/test_embedding_model.py -v
```

Expected: `ModuleNotFoundError: No module named 'app.search.embedding_model'`

- [ ] **Step 3: Create `embedding_model.py` with protocol and local backend**

```python
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
        return [v.tolist() for v in vectors]

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
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
cd /Users/sairamugge/Desktop/Not-Humans-World/Tombstone/services/intelligence
python -m pytest tests/test_embedding_model.py -v
```

Expected: `3 passed`

- [ ] **Step 5: Commit**

```bash
cd /Users/sairamugge/Desktop/Not-Humans-World/Tombstone
git add services/intelligence/app/search/embedding_model.py services/intelligence/tests/test_embedding_model.py
git commit -m "feat(intelligence): add EmbeddingModel protocol + LocalEmbeddingModel factory"
```

---

## Task 2: Bedrock Embedding Backend

**Files:**
- Create: `services/intelligence/app/search/embedding_model_bedrock.py`
- Modify: `services/intelligence/tests/test_embedding_model.py` (add Bedrock tests)

**Interfaces:**
- Consumes: `EmbeddingModel` protocol from Task 1
- Produces: `BedrockEmbeddingModel(access_key_id, secret_access_key, region)` with `.embed(texts)` calling `bedrock-runtime.invoke_model`

- [ ] **Step 1: Add Bedrock tests to the test file**

Append to `services/intelligence/tests/test_embedding_model.py`:

```python
@pytest.mark.asyncio
async def test_bedrock_embedding_model_embed_returns_1024_dims():
    """BedrockEmbeddingModel.embed() calls invoke_model and returns 1024-dim vectors."""
    import json
    from unittest.mock import AsyncMock
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
```

- [ ] **Step 2: Run tests to verify new tests fail**

```bash
cd /Users/sairamugge/Desktop/Not-Humans-World/Tombstone/services/intelligence
python -m pytest tests/test_embedding_model.py -v
```

Expected: 3 pass (Task 1), 3 fail with `ModuleNotFoundError: app.search.embedding_model_bedrock`

- [ ] **Step 3: Create `embedding_model_bedrock.py`**

```python
# services/intelligence/app/search/embedding_model_bedrock.py
from __future__ import annotations

import asyncio
import json
import logging

logger = logging.getLogger(__name__)

_MODEL_ID = "amazon.titan-embed-text-v2:0"
_DIMENSIONS = 1024


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
        self._access_key_id = access_key_id
        self._secret_access_key = secret_access_key
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
```

- [ ] **Step 4: Run all embedding tests**

```bash
cd /Users/sairamugge/Desktop/Not-Humans-World/Tombstone/services/intelligence
python -m pytest tests/test_embedding_model.py -v
```

Expected: `6 passed`

- [ ] **Step 5: Commit**

```bash
cd /Users/sairamugge/Desktop/Not-Humans-World/Tombstone
git add services/intelligence/app/search/embedding_model_bedrock.py services/intelligence/tests/test_embedding_model.py
git commit -m "feat(intelligence): add BedrockEmbeddingModel using Titan V2 — 1024-dim, reuses Anvilry AWS creds"
```

---

## Task 3: Wire EmbeddingModel into retriever.py and embedding_sync.py

**Files:**
- Modify: `services/intelligence/app/search/retriever.py`
- Modify: `services/intelligence/app/search/embedding_sync.py`

**Interfaces:**
- Consumes: `EmbeddingModel` protocol from Task 1 — specifically `.embed(texts: list[str]) -> list[list[float]]`
- Produces: `FlagSearchRetriever(db_url, embedding_model: EmbeddingModel)` — accepts injected model
- Produces: `EmbeddingSyncService(db_url, embedding_model: EmbeddingModel)` — accepts injected model

- [ ] **Step 1: Modify `FlagSearchRetriever.__init__` to accept injected model**

In `services/intelligence/app/search/retriever.py`, make these changes:

Change the class signature and `__init__`:

```python
# BEFORE (around line 10-20):
class FlagSearchRetriever:
    MODEL_NAME = "BAAI/bge-m3"
    _RRF_K = 60

    def __init__(self, db_url: str) -> None:
        self._db_url: str = db_url
        self._pool: asyncpg.Pool | None = None
        self._model: SentenceTransformer | None = None

    async def initialize(self) -> None:
        self._pool = await asyncpg.create_pool(self._db_url)
        try:
            self._model = SentenceTransformer(self.MODEL_NAME)
            logger.info("BGE-M3 model loaded — dense vector search enabled")
        except Exception as exc:
            logger.warning("BGE-M3 model unavailable (%s) — falling back to lexical-only search", exc)
            self._model = None

# AFTER:
from app.search.embedding_model import EmbeddingModel  # add this import at top

class FlagSearchRetriever:
    _RRF_K = 60

    def __init__(self, db_url: str, embedding_model: EmbeddingModel | None = None) -> None:
        self._db_url: str = db_url
        self._pool: asyncpg.Pool | None = None
        self._embedding_model: EmbeddingModel | None = embedding_model

    async def initialize(self) -> None:
        self._pool = await asyncpg.create_pool(self._db_url)
        if self._embedding_model is not None:
            await self._embedding_model.initialize()
            logger.info("FlagSearchRetriever: embedding model initialized")
        else:
            logger.warning("FlagSearchRetriever: no embedding model — dense search disabled")
```

Find the method that calls `self._model.encode(...)` (the dense search arm in `search()`). Replace it:

```python
# BEFORE (wherever self._model.encode appears in search()):
if self._model is not None:
    query_vec = self._model.encode([query])[0].tolist()
    # ... pgvector query using query_vec

# AFTER:
if self._embedding_model is not None:
    vecs = await self._embedding_model.embed([query])
    query_vec = vecs[0] if vecs and vecs[0] else None
    if query_vec:
        # ... pgvector query using query_vec
```

- [ ] **Step 2: Modify `EmbeddingSyncService.__init__` to accept injected model**

In `services/intelligence/app/search/embedding_sync.py`, change:

```python
# BEFORE (around line 26-50):
class EmbeddingSyncService:
    def __init__(self, db_url: str) -> None:
        self._db_url: str = db_url
        self._pool: asyncpg.Pool | None = None
        self._model: SentenceTransformer | None = None

    async def initialize(self) -> None:
        self._pool = await asyncpg.create_pool(self._db_url)
        self._model = await loop.run_in_executor(None, lambda: SentenceTransformer(_MODEL_NAME))
        asyncio.create_task(self._backfill())

# AFTER:
from app.search.embedding_model import EmbeddingModel  # add this import at top

class EmbeddingSyncService:
    def __init__(self, db_url: str, embedding_model: EmbeddingModel | None = None) -> None:
        self._db_url: str = db_url
        self._pool: asyncpg.Pool | None = None
        self._embedding_model: EmbeddingModel | None = embedding_model

    async def initialize(self) -> None:
        self._pool = await asyncpg.create_pool(self._db_url)
        if self._embedding_model is not None:
            await self._embedding_model.initialize()
        asyncio.create_task(self._backfill())
```

Find wherever `self._model.encode(...)` is called inside `on_flag_event` and `_backfill`. Replace all occurrences:

```python
# BEFORE (any call like):
embedding = self._model.encode([text])[0].tolist()

# AFTER:
if self._embedding_model is None:
    return  # skip embedding if no model configured
vecs = await self._embedding_model.embed([text])
embedding = vecs[0] if vecs and vecs[0] else None
if embedding is None:
    return
```

- [ ] **Step 3: Verify import syntax is correct**

```bash
cd /Users/sairamugge/Desktop/Not-Humans-World/Tombstone/services/intelligence
python -c "from app.search.retriever import FlagSearchRetriever; from app.search.embedding_sync import EmbeddingSyncService; print('imports OK')"
```

Expected: `imports OK`

- [ ] **Step 4: Commit**

```bash
cd /Users/sairamugge/Desktop/Not-Humans-World/Tombstone
git add services/intelligence/app/search/retriever.py services/intelligence/app/search/embedding_sync.py
git commit -m "refactor(intelligence): inject EmbeddingModel into FlagSearchRetriever + EmbeddingSyncService — decouples from SentenceTransformer"
```

---

## Task 4: Wire embedding factory into `main.py`

**Files:**
- Modify: `services/intelligence/app/main.py`

**Interfaces:**
- Consumes: `create_embedding_model(backend, **kwargs)` from `embedding_model.py`
- Consumes: `EMBEDDING_BACKEND` env var (`local` | `bedrock`, default `local`)
- Consumes: `BEDROCK_ACCESS_KEY_ID`, `BEDROCK_SECRET_ACCESS_KEY`, `BEDROCK_REGION` env vars (only when backend=bedrock)

- [ ] **Step 1: Add embedding model creation to the lifespan startup block**

In `services/intelligence/app/main.py`, find the `lifespan` function startup section (around line 68–80 where `app.state.searcher` and `app.state.embedding_sync` are created).

Add these lines **before** those two lines:

```python
# Build embedding model — swap EMBEDDING_BACKEND=bedrock for Fly.io free tier
import os as _os
from app.search.embedding_model import create_embedding_model as _create_embedding_model

_embedding_backend = _os.environ.get("EMBEDDING_BACKEND", "local")
if _embedding_backend == "bedrock":
    _embedding_model = _create_embedding_model(
        "bedrock",
        access_key_id=_os.environ["BEDROCK_ACCESS_KEY_ID"],
        secret_access_key=_os.environ["BEDROCK_SECRET_ACCESS_KEY"],
        region=_os.environ.get("BEDROCK_REGION", "us-east-1"),
    )
else:
    _embedding_model = _create_embedding_model("local")

app.state.embedding_model = _embedding_model
```

Then change the next two lines (the ones that create `searcher` and `embedding_sync`) to pass the model:

```python
# BEFORE:
app.state.searcher = FlagSearchRetriever(db_url=os.environ["DB_URL"])
app.state.embedding_sync = EmbeddingSyncService(db_url=os.environ["DB_URL"])

# AFTER:
app.state.searcher = FlagSearchRetriever(
    db_url=os.environ["DB_URL"],
    embedding_model=app.state.embedding_model,
)
app.state.embedding_sync = EmbeddingSyncService(
    db_url=os.environ["DB_URL"],
    embedding_model=app.state.embedding_model,
)
```

Add `create_embedding_model` to the imports at the top of `main.py`:

```python
from app.search.embedding_model import create_embedding_model
```

Remove the duplicate `import os as _os` inside the function — use the top-level `import os` that already exists.

- [ ] **Step 2: Verify the app starts without errors**

```bash
cd /Users/sairamugge/Desktop/Not-Humans-World/Tombstone/services/intelligence
EMBEDDING_BACKEND=local DB_URL=postgresql://dummy:dummy@localhost/dummy REDIS_URL=redis://localhost:6379 python -c "
import asyncio, os
os.environ.setdefault('KAFKA_BROKERS', 'localhost:9092')
from app.search.embedding_model import create_embedding_model
m = create_embedding_model('local')
print('factory OK:', type(m).__name__)
"
```

Expected: `factory OK: LocalEmbeddingModel`

- [ ] **Step 3: Commit**

```bash
cd /Users/sairamugge/Desktop/Not-Humans-World/Tombstone
git add services/intelligence/app/main.py
git commit -m "feat(intelligence): wire EMBEDDING_BACKEND env var into main.py lifespan — local or bedrock at startup"
```

---

## Task 5: `EventConsumer` ABC + `KafkaEventConsumer` wrapper

**Files:**
- Modify: `services/intelligence/app/kafka/consumer.py`
- Create: `services/intelligence/tests/test_consumer_factory.py`

**Interfaces:**
- Produces: `EventConsumer` ABC with `async def start()`, `async def stop()`, `def __aiter__(self)` yielding `(topic: str, payload: dict)`
- Produces: `KafkaEventConsumer(brokers, anomaly_detector, embedding_sync, graph_builder, redis_client)` — wraps existing `TelemetryConsumer`
- Produces: `create_consumer(backend: str, **kwargs) -> EventConsumer` factory

- [ ] **Step 1: Write failing tests**

```python
# services/intelligence/tests/test_consumer_factory.py
from __future__ import annotations

import pytest
from unittest.mock import MagicMock, AsyncMock, patch


def test_create_consumer_returns_kafka_by_default():
    from app.kafka.consumer import create_consumer, KafkaEventConsumer
    consumer = create_consumer(
        "kafka",
        brokers="localhost:9092",
        anomaly_detector=MagicMock(),
    )
    assert isinstance(consumer, KafkaEventConsumer)


def test_create_consumer_returns_redis_streams():
    from app.kafka.consumer import create_consumer, RedisStreamsEventConsumer
    consumer = create_consumer(
        "redis",
        redis_url="redis://localhost:6379",
        anomaly_detector=MagicMock(),
        environments=["production"],
    )
    assert isinstance(consumer, RedisStreamsEventConsumer)


def test_create_consumer_unknown_backend_raises():
    from app.kafka.consumer import create_consumer
    with pytest.raises(ValueError, match="Unknown consumer backend"):
        create_consumer("rabbitmq", brokers="x", anomaly_detector=MagicMock())


@pytest.mark.asyncio
async def test_redis_consumer_start_creates_groups():
    """RedisStreamsEventConsumer.start() calls XGROUP CREATE on each stream."""
    from app.kafka.consumer import RedisStreamsEventConsumer

    mock_redis = AsyncMock()
    mock_redis.xgroup_create = AsyncMock(return_value="OK")

    consumer = RedisStreamsEventConsumer(
        redis_url="redis://localhost:6379",
        anomaly_detector=MagicMock(),
        environments=["production", "staging"],
    )
    consumer._redis = mock_redis  # inject mock

    await consumer.start()

    assert mock_redis.xgroup_create.call_count == 2
    calls = [c.args[0] for c in mock_redis.xgroup_create.call_args_list]
    assert "tombstone:stream:production" in calls
    assert "tombstone:stream:staging" in calls
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
cd /Users/sairamugge/Desktop/Not-Humans-World/Tombstone/services/intelligence
python -m pytest tests/test_consumer_factory.py -v
```

Expected: `ImportError` — `create_consumer`, `KafkaEventConsumer`, `RedisStreamsEventConsumer` don't exist yet

- [ ] **Step 3: Add the ABC, wrapper, and factory to `consumer.py`**

At the **top** of `services/intelligence/app/kafka/consumer.py`, add these imports and classes (before the existing `TelemetryConsumer` class):

```python
# Add at top of file, after existing imports:
from abc import ABC, abstractmethod


class EventConsumer(ABC):
    """Abstract base for all event consumer backends."""

    @abstractmethod
    async def start(self) -> None:
        """Initialize connections and consumer groups."""
        ...

    @abstractmethod
    async def stop(self) -> None:
        """Gracefully shut down and release resources."""
        ...

    @abstractmethod
    def __aiter__(self):
        """Yield (topic: str, payload: dict) tuples."""
        ...


class KafkaEventConsumer(EventConsumer):
    """
    Thin wrapper around the existing TelemetryConsumer.
    Preserves all original Kafka behaviour unchanged.
    """

    def __init__(
        self,
        brokers: str,
        anomaly_detector,
        embedding_sync=None,
        graph_builder=None,
        redis_client=None,
    ) -> None:
        self._inner = TelemetryConsumer(
            brokers=brokers,
            anomaly_detector=anomaly_detector,
            embedding_sync=embedding_sync,
            graph_builder=graph_builder,
            redis_client=redis_client,
        )

    async def start(self) -> None:
        pass  # TelemetryConsumer.run() handles its own startup

    async def stop(self) -> None:
        if self._inner._consumer is not None:
            await self._inner._consumer.stop()

    def __aiter__(self):
        # Not used — KafkaEventConsumer drives itself via run()
        return iter([])

    async def run(self):
        """Delegate to the existing TelemetryConsumer.run() loop."""
        await self._inner.run()
```

At the **bottom** of `consumer.py`, add the factory function:

```python
def create_consumer(backend: str, **kwargs) -> "EventConsumer":
    """
    Factory for event consumer backends.

    backend='kafka' kwargs: brokers, anomaly_detector, embedding_sync, graph_builder, redis_client
    backend='redis' kwargs: redis_url, anomaly_detector, environments, embedding_sync, graph_builder
    """
    if backend == "kafka":
        return KafkaEventConsumer(
            brokers=kwargs["brokers"],
            anomaly_detector=kwargs["anomaly_detector"],
            embedding_sync=kwargs.get("embedding_sync"),
            graph_builder=kwargs.get("graph_builder"),
            redis_client=kwargs.get("redis_client"),
        )
    if backend == "redis":
        return RedisStreamsEventConsumer(
            redis_url=kwargs["redis_url"],
            anomaly_detector=kwargs["anomaly_detector"],
            environments=kwargs.get("environments", ["production"]),
            embedding_sync=kwargs.get("embedding_sync"),
            graph_builder=kwargs.get("graph_builder"),
        )
    raise ValueError(f"Unknown consumer backend: {backend!r}. Choose 'kafka' or 'redis'.")
```

- [ ] **Step 4: Run tests to verify factory tests pass (Redis consumer still missing)**

```bash
cd /Users/sairamugge/Desktop/Not-Humans-World/Tombstone/services/intelligence
python -m pytest tests/test_consumer_factory.py::test_create_consumer_returns_kafka_by_default tests/test_consumer_factory.py::test_create_consumer_unknown_backend_raises -v
```

Expected: `2 passed`, Redis tests still fail

- [ ] **Step 5: Commit the ABC and Kafka wrapper**

```bash
cd /Users/sairamugge/Desktop/Not-Humans-World/Tombstone
git add services/intelligence/app/kafka/consumer.py services/intelligence/tests/test_consumer_factory.py
git commit -m "feat(intelligence): add EventConsumer ABC + KafkaEventConsumer wrapper + create_consumer() factory"
```

---

## Task 6: `RedisStreamsEventConsumer`

**Files:**
- Modify: `services/intelligence/app/kafka/consumer.py` (add `RedisStreamsEventConsumer` class)

**Interfaces:**
- Consumes: `EventConsumer` ABC from Task 5
- Consumes: `redis.asyncio` client (already a dep: `redis[asyncio]>=5.0.0`)
- Consumes: `tombstone:stream:{environment}` Redis Streams (written by flag-api Phase 4.1)
- Produces: `RedisStreamsEventConsumer.run()` — equivalent infinite loop to `TelemetryConsumer.run()`; feeds same `anomaly_detector` and `graph_builder`

- [ ] **Step 1: Add `RedisStreamsEventConsumer` to `consumer.py`**

Add this class **after** `KafkaEventConsumer` and **before** `create_consumer`:

```python
class RedisStreamsEventConsumer(EventConsumer):
    """
    Reads from tombstone:stream:{environment} Redis Streams via XREADGROUP.
    Replaces aiokafka for Fly.io free-tier deployment.
    Consumer group: intelligence-worker  (distinct from gateway's gateway-workers).
    At-least-once delivery via XACK + Pending Entries List.
    """

    _GROUP = "intelligence-worker"
    _BLOCK_MS = 1000   # 1s block timeout — yields control to event loop
    _COUNT = 10        # messages per XREADGROUP call

    def __init__(
        self,
        redis_url: str,
        anomaly_detector,
        environments: list[str] | None = None,
        embedding_sync=None,
        graph_builder=None,
    ) -> None:
        self._redis_url = redis_url
        self._detector = anomaly_detector
        self._environments = environments or ["production"]
        self._embedding_sync = embedding_sync
        self._graph_builder = graph_builder
        self._redis = None
        self._running = False
        # Build stream keys from environment names — must match flag-api format
        self._streams = [f"tombstone:stream:{env}" for env in self._environments]

    async def start(self) -> None:
        import redis.asyncio as aioredis
        self._redis = aioredis.from_url(self._redis_url, decode_responses=True)
        # Create consumer groups idempotently (BUSYGROUP = already exists = OK)
        for stream in self._streams:
            try:
                await self._redis.xgroup_create(
                    stream, self._GROUP, id="$", mkstream=True
                )
                logger.info("RedisStreamsEventConsumer: created group %s on %s", self._GROUP, stream)
            except Exception as exc:
                if "BUSYGROUP" in str(exc):
                    logger.debug("RedisStreamsEventConsumer: group %s already exists on %s", self._GROUP, stream)
                else:
                    logger.warning("RedisStreamsEventConsumer: xgroup_create error on %s: %s", stream, exc)

    async def stop(self) -> None:
        self._running = False
        if self._redis is not None:
            await self._redis.aclose()
            self._redis = None

    def __aiter__(self):
        return self

    async def __anext__(self):
        raise StopAsyncIteration  # run() drives itself; __aiter__ is for protocol compliance

    async def run(self) -> None:
        """
        Infinite loop: XREADGROUP → dispatch to detector/graph_builder → XACK.
        Mirrors TelemetryConsumer.run() semantics so main.py can treat both identically.
        """
        import os
        consumer_name = f"intelligence-{os.environ.get('FLY_MACHINE_ID', 'local')}"
        self._running = True
        logger.info("RedisStreamsEventConsumer: starting on streams %s as %s", self._streams, consumer_name)

        # Window buffer: same pattern as TelemetryConsumer._window
        window: dict[str, dict] = {}   # "flag_key:env" -> {"errors": int, "total": int}
        flush_interval = 10.0
        last_flush = asyncio.get_event_loop().time()

        while self._running:
            try:
                # Read from all streams in one call
                stream_args = {s: ">" for s in self._streams}
                results = await self._redis.xreadgroup(
                    self._GROUP,
                    consumer_name,
                    stream_args,
                    count=self._COUNT,
                    block=self._BLOCK_MS,
                )

                for stream_key, messages in (results or []):
                    for msg_id, data in messages:
                        await self._dispatch(data, window)
                        await self._redis.xack(stream_key, self._GROUP, msg_id)

                # Flush detector every 10s (same cadence as TelemetryConsumer._flush_loop)
                now = asyncio.get_event_loop().time()
                if now - last_flush >= flush_interval:
                    await self._flush(window)
                    window.clear()
                    last_flush = now

            except asyncio.CancelledError:
                break
            except Exception as exc:
                logger.warning("RedisStreamsEventConsumer: read error: %s", exc)
                await asyncio.sleep(1.0)

        await self.stop()

    async def _dispatch(self, data: dict, window: dict) -> None:
        """Route a stream message to the right handler based on event type."""
        event_type = data.get("event", "")

        # flag.evaluated → accumulate in window for anomaly detection
        if event_type in ("flag_evaluated", "FALLTHROUGH", "RULE_MATCH", "OFF", "TARGET_MATCH"):
            flag_key = data.get("flag_key", "")
            environment = data.get("environment", "production")
            is_error = data.get("is_error") in ("true", "True", "1", True)
            if flag_key:
                wk = f"{flag_key}:{environment}"
                if wk not in window:
                    window[wk] = {"errors": 0, "total": 0}
                window[wk]["total"] += 1
                if is_error:
                    window[wk]["errors"] += 1

        # flag.created or flag.updated → trigger embedding sync
        elif event_type in ("flag_created", "flag_environment_updated", "kill_switch_activated"):
            flag_key = data.get("flag_key", "")
            if flag_key and self._embedding_sync is not None:
                payload_str = data.get("payload", "{}")
                try:
                    import json as _json
                    payload = _json.loads(payload_str) if isinstance(payload_str, str) else payload_str
                    await self._embedding_sync.on_flag_event(
                        event_type=event_type,
                        flag_key=flag_key,
                        name=payload.get("name", flag_key),
                        description=payload.get("description", ""),
                        tags=payload.get("tags", []),
                    )
                except Exception as exc:
                    logger.warning("RedisStreamsEventConsumer: embedding sync failed for %s: %s", flag_key, exc)

            # Also update dep graph
            if flag_key and self._graph_builder is not None and hasattr(self, "_redis") and self._redis:
                try:
                    from datetime import datetime, timezone
                    environment = data.get("environment", "production")
                    await self._graph_builder.update_on_flag_change(
                        flag_key=flag_key,
                        environment=environment,
                        changed_at=datetime.now(timezone.utc),
                        redis_client=self._redis,
                    )
                except Exception as exc:
                    logger.warning("RedisStreamsEventConsumer: graph update failed: %s", exc)

    async def _flush(self, window: dict) -> None:
        """Flush accumulated window counts to the anomaly detector."""
        for wk, counts in window.items():
            if counts["total"] > 0:
                flag_key = wk.split(":")[0]
                self._detector.record(flag_key, counts["errors"], counts["total"])
```

- [ ] **Step 2: Run all consumer factory tests**

```bash
cd /Users/sairamugge/Desktop/Not-Humans-World/Tombstone/services/intelligence
python -m pytest tests/test_consumer_factory.py -v
```

Expected: `4 passed`

- [ ] **Step 3: Commit**

```bash
cd /Users/sairamugge/Desktop/Not-Humans-World/Tombstone
git add services/intelligence/app/kafka/consumer.py
git commit -m "feat(intelligence): add RedisStreamsEventConsumer — reads tombstone:stream:{env} via XREADGROUP, replaces aiokafka for Fly.io"
```

---

## Task 7: Wire `CONSUMER_BACKEND` into `main.py`

**Files:**
- Modify: `services/intelligence/app/main.py`

**Interfaces:**
- Consumes: `create_consumer(backend, **kwargs)` from `consumer.py`
- Consumes: `CONSUMER_BACKEND` env var (`kafka` | `redis`, default `kafka`)
- Consumes: `REDIS_URL` env var (already used in main.py for Redis client)

- [ ] **Step 1: Replace direct `TelemetryConsumer` instantiation with factory call**

In `services/intelligence/app/main.py`, find the section that creates the Kafka consumer (around line 128–135):

```python
# BEFORE:
consumer = TelemetryConsumer(
    brokers=os.environ.get("KAFKA_BROKERS", "localhost:9092"),
    anomaly_detector=app.state.anomaly,
    embedding_sync=app.state.embedding_sync,
    graph_builder=app.state.graph_builder if app.state.redis else None,
    redis_client=app.state.redis,
)
consumer_task = asyncio.create_task(consumer.run())

# AFTER:
from app.kafka.consumer import create_consumer as _create_consumer

_consumer_backend = os.environ.get("CONSUMER_BACKEND", "kafka")
if _consumer_backend == "redis":
    _consumer = _create_consumer(
        "redis",
        redis_url=os.environ.get("REDIS_URL", "redis://localhost:6379"),
        anomaly_detector=app.state.anomaly,
        environments=os.environ.get("TOMBSTONE_ENVIRONMENTS", "production").split(","),
        embedding_sync=app.state.embedding_sync,
        graph_builder=app.state.graph_builder if app.state.redis else None,
    )
    await _consumer.start()
else:
    _consumer = _create_consumer(
        "kafka",
        brokers=os.environ.get("KAFKA_BROKERS", "localhost:9092"),
        anomaly_detector=app.state.anomaly,
        embedding_sync=app.state.embedding_sync,
        graph_builder=app.state.graph_builder if app.state.redis else None,
        redis_client=app.state.redis,
    )

consumer_task = asyncio.create_task(_consumer.run())
```

Also update the shutdown section to call `_consumer.stop()`:

Find the existing `consumer_task.cancel()` in the shutdown block and add before it:

```python
    await _consumer.stop()
    consumer_task.cancel()
```

- [ ] **Step 2: Add `create_consumer` to main.py imports**

At the top of `main.py`, add:

```python
from app.kafka.consumer import create_consumer
```

- [ ] **Step 3: Verify the app can start with both backends (syntax check)**

```bash
cd /Users/sairamugge/Desktop/Not-Humans-World/Tombstone/services/intelligence
python -c "
import ast, sys
with open('app/main.py') as f:
    source = f.read()
try:
    ast.parse(source)
    print('main.py: syntax OK')
except SyntaxError as e:
    print(f'SYNTAX ERROR: {e}')
    sys.exit(1)
"
```

Expected: `main.py: syntax OK`

- [ ] **Step 4: Run all tests to confirm nothing broken**

```bash
cd /Users/sairamugge/Desktop/Not-Humans-World/Tombstone/services/intelligence
python -m pytest tests/ -v --ignore=tests/test_integration.py 2>/dev/null || python -m pytest tests/ -v
```

Expected: All tests pass (at minimum `test_embedding_model.py` and `test_consumer_factory.py`)

- [ ] **Step 5: Commit**

```bash
cd /Users/sairamugge/Desktop/Not-Humans-World/Tombstone
git add services/intelligence/app/main.py
git commit -m "feat(intelligence): wire CONSUMER_BACKEND env var — kafka (default) or redis for Fly.io free tier"
```

---

## Task 8: Re-embedding script + Fly.io config files

**Files:**
- Create: `services/intelligence/scripts/reembed_flags.py`
- Create: `services/intelligence/fly.toml`
- Modify: `services/intelligence/pyproject.toml` (add boto3 as optional dep)

**Interfaces:**
- Produces: one-shot async script that re-embeds all flags using the configured backend
- Produces: `fly.toml` for deploying the intelligence service to Fly.io with correct env vars

- [ ] **Step 1: Create the re-embedding migration script**

```python
# services/intelligence/scripts/reembed_flags.py
"""
One-shot migration script: re-embed all flags using the configured EMBEDDING_BACKEND.
Run ONCE after deploying Bedrock backend to populate pgvector with Titan V2 vectors.

Usage:
    cd services/intelligence
    EMBEDDING_BACKEND=bedrock \
    BEDROCK_ACCESS_KEY_ID=<key> \
    BEDROCK_SECRET_ACCESS_KEY=<secret> \
    BEDROCK_REGION=us-east-1 \
    DB_URL=postgresql://... \
    python scripts/reembed_flags.py
"""
from __future__ import annotations

import asyncio
import os
import sys
import logging

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

logging.basicConfig(level=logging.INFO, format="%(levelname)s %(message)s")
logger = logging.getLogger(__name__)

BATCH_SIZE = 50


async def main() -> None:
    import asyncpg
    from app.search.embedding_model import create_embedding_model

    db_url = os.environ["DB_URL"]
    backend = os.environ.get("EMBEDDING_BACKEND", "local")

    if backend == "bedrock":
        model = create_embedding_model(
            "bedrock",
            access_key_id=os.environ["BEDROCK_ACCESS_KEY_ID"],
            secret_access_key=os.environ["BEDROCK_SECRET_ACCESS_KEY"],
            region=os.environ.get("BEDROCK_REGION", "us-east-1"),
        )
    else:
        model = create_embedding_model("local")

    await model.initialize()
    pool = await asyncpg.create_pool(db_url)

    # Count flags needing re-embedding
    total = await pool.fetchval("SELECT COUNT(*) FROM flags")
    logger.info("Re-embedding %d flags using backend=%s", total, backend)

    offset = 0
    processed = 0

    while True:
        rows = await pool.fetch(
            """
            SELECT key, name, description, tags
            FROM flags
            ORDER BY created_at
            LIMIT $1 OFFSET $2
            """,
            BATCH_SIZE, offset,
        )
        if not rows:
            break

        texts = [
            f"{row['name']} {row['description']} {' '.join(row['tags'] or [])}"
            for row in rows
        ]
        keys = [row["key"] for row in rows]

        try:
            vectors = await model.embed(texts)
            for key, vec in zip(keys, vectors):
                if vec:
                    await pool.execute(
                        "UPDATE flags SET embedding = $1::vector WHERE key = $2",
                        vec, key,
                    )
                    processed += 1
        except Exception as exc:
            logger.error("Batch at offset %d failed: %s", offset, exc)

        offset += BATCH_SIZE
        logger.info("Progress: %d/%d", processed, total)

    await pool.close()
    await model.close()
    logger.info("Done. %d flags re-embedded.", processed)


if __name__ == "__main__":
    asyncio.run(main())
```

- [ ] **Step 2: Create `fly.toml` for the intelligence service**

```toml
# services/intelligence/fly.toml
# Fly.io deployment config for Tombstone intelligence service.
# Free tier: shared-cpu-1x, 256MB RAM (fits without ML model).
# Set secrets via: fly secrets set BEDROCK_ACCESS_KEY_ID=... etc.

app = "tombstone-intelligence"
primary_region = "iad"   # us-east (matches BEDROCK_REGION=us-east-1)

[build]
dockerfile = "Dockerfile"

[env]
PORT = "8083"
EMBEDDING_BACKEND = "bedrock"
CONSUMER_BACKEND = "redis"
TOMBSTONE_ENVIRONMENTS = "production"
# Secrets (set via fly secrets set, never commit):
# BEDROCK_ACCESS_KEY_ID
# BEDROCK_SECRET_ACCESS_KEY
# BEDROCK_REGION = "us-east-1"
# DB_URL (Neon free PostgreSQL)
# REDIS_URL (Upstash free Redis)

[[services]]
internal_port = 8083
protocol = "tcp"

[[services.ports]]
port = 443
handlers = ["tls", "http"]

[[services.ports]]
port = 80
handlers = ["http"]

[services.concurrency]
type = "connections"
hard_limit = 25
soft_limit = 20

[[vm]]
size = "shared-cpu-1x"
memory = "256mb"
```

- [ ] **Step 3: Add boto3 as optional dep in pyproject.toml**

In `services/intelligence/pyproject.toml`, find `[project.optional-dependencies]` and add:

```toml
[project.optional-dependencies]
# ... existing entries ...
bedrock = ["boto3>=1.34.0"]
```

This keeps boto3 out of the default install. On Fly.io, install with `pip install -e ".[bedrock]"` or add boto3 directly to the Dockerfile.

- [ ] **Step 4: Verify re-embedding script syntax**

```bash
cd /Users/sairamugge/Desktop/Not-Humans-World/Tombstone/services/intelligence
python -c "
import ast
with open('scripts/reembed_flags.py') as f:
    ast.parse(f.read())
print('reembed_flags.py: syntax OK')
"
```

Expected: `reembed_flags.py: syntax OK`

- [ ] **Step 5: Commit all**

```bash
cd /Users/sairamugge/Desktop/Not-Humans-World/Tombstone
git add services/intelligence/scripts/reembed_flags.py services/intelligence/fly.toml services/intelligence/pyproject.toml
git commit -m "feat(intelligence): add re-embedding migration script + fly.toml + boto3 optional dep"
```

---

## Task 9: Final integration test + PR

**Files:**
- None new — integration test using existing stack

- [ ] **Step 1: Run full test suite**

```bash
cd /Users/sairamugge/Desktop/Not-Humans-World/Tombstone/services/intelligence
python -m pytest tests/ -v
```

Expected: All tests pass. Minimum: `tests/test_embedding_model.py` (6 tests) + `tests/test_consumer_factory.py` (4 tests) = 10 tests passing.

- [ ] **Step 2: Verify import chain end-to-end**

```bash
cd /Users/sairamugge/Desktop/Not-Humans-World/Tombstone/services/intelligence
python -c "
import os
os.environ['EMBEDDING_BACKEND'] = 'local'
os.environ['CONSUMER_BACKEND'] = 'kafka'
from app.search.embedding_model import create_embedding_model
from app.kafka.consumer import create_consumer
from unittest.mock import MagicMock

m = create_embedding_model('local')
c = create_consumer('kafka', brokers='localhost:9092', anomaly_detector=MagicMock())
print('local/kafka factory chain: OK')

m2 = create_embedding_model('bedrock', access_key_id='k', secret_access_key='s', region='us-east-1')
c2 = create_consumer('redis', redis_url='redis://x', anomaly_detector=MagicMock(), environments=['production'])
print('bedrock/redis factory chain: OK')
print()
print('Memory profile (bedrock+redis, no ML model loaded):')
import tracemalloc
tracemalloc.start()
# factories instantiated above
current, peak = tracemalloc.get_traced_memory()
print(f'  current: {current/1024:.1f} KB, peak: {peak/1024:.1f} KB')
print('  (ML model NOT loaded — stays at ~0 until initialize() is called)')
"
```

Expected output:
```
local/kafka factory chain: OK
bedrock/redis factory chain: OK

Memory profile (bedrock+redis, no ML model loaded):
  current: ~50-200 KB, peak: ~50-200 KB
  (ML model NOT loaded — stays at ~0 until initialize() is called)
```

- [ ] **Step 3: Open PR to develop**

```bash
cd /Users/sairamugge/Desktop/Not-Humans-World/Tombstone
gh pr create \
  --base develop \
  --title "feat(intelligence): Bedrock embeddings + Redis Streams consumer — Fly.io free-tier deployment" \
  --body "$(cat <<'EOF'
## Summary

Enables the intelligence service to run on Fly.io free tier (256MB) by replacing two heavyweight dependencies with cloud-native alternatives, both gated behind factory env vars.

### Bedrock Embeddings (EMBEDDING_BACKEND=bedrock)
- Replaces 1.4GB BAAI/bge-m3 local model with AWS Bedrock Titan Text Embeddings V2
- Same 1024-dim output — no pgvector schema migration required, only a re-embedding pass
- Reuses existing BEDROCK_ACCESS_KEY_ID/SECRET/REGION credentials from Anvilry
- Cost: ~$0.001/month at Tombstone's flag-update volume
- Factory pattern: LocalEmbeddingModel (default) or BedrockEmbeddingModel

### Redis Streams Consumer (CONSUMER_BACKEND=redis)
- Replaces aiokafka infinite loop with aioredis XREADGROUP on existing tombstone:stream:{env} streams
- At-least-once delivery via XACK + PEL (same semantics as Kafka)
- Uses existing Upstash Redis — $0 additional cost
- Consumer group: intelligence-worker (distinct from gateway-workers)
- Factory pattern: KafkaEventConsumer (default) or RedisStreamsEventConsumer

### Memory impact
| Before | After |
|--------|-------|
| ~1.8GB (model + Kafka deps) | ~250MB (API calls only) |

## Migration steps for Fly.io
1. `fly secrets set BEDROCK_ACCESS_KEY_ID=... BEDROCK_SECRET_ACCESS_KEY=... BEDROCK_REGION=us-east-1`
2. `fly secrets set DB_URL=... REDIS_URL=...`
3. `fly deploy`
4. Run `python scripts/reembed_flags.py` once to re-embed existing flags with Titan V2

## Local dev unchanged
Default `EMBEDDING_BACKEND=local` and `CONSUMER_BACKEND=kafka` preserve all existing behaviour.
EOF
)"
```

---

## Verification Summary

After all tasks complete, confirm:

```bash
# 1. Tests pass
cd services/intelligence && python -m pytest tests/ -v
# Expect: 10+ tests passing

# 2. Both factory chains work without errors
EMBEDDING_BACKEND=local CONSUMER_BACKEND=kafka python -c "
from app.search.embedding_model import create_embedding_model
from app.kafka.consumer import create_consumer
from unittest.mock import MagicMock
create_embedding_model('local')
create_consumer('kafka', brokers='x', anomaly_detector=MagicMock())
print('OK')
"

# 3. No SentenceTransformer import at module level (model not loaded until initialize())
python -c "
import sys
# Should not trigger model download just from importing
from app.search.retriever import FlagSearchRetriever
from app.search.embedding_sync import EmbeddingSyncService
print('No model loaded on import: OK')
"

# 4. Fly.toml is present
ls services/intelligence/fly.toml && echo "fly.toml: OK"

# 5. Re-embedding script is present
ls services/intelligence/scripts/reembed_flags.py && echo "reembed_flags.py: OK"
```
