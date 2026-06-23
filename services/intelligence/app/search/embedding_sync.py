"""
Embedding sync utilities for the Tombstone intelligence service.

Provides:
  - sync_flag_embedding()   — update a single flag's vector in PostgreSQL
  - EmbeddingSyncService    — backfill on startup + process flag.created/flag.updated events
"""

import asyncio
import logging

import asyncpg
from sentence_transformers import SentenceTransformer

logger = logging.getLogger(__name__)

_BATCH_SIZE = 50
_MODEL_NAME = "BAAI/bge-m3"


# ---------------------------------------------------------------------------
# Low-level helper (also callable from routes / Kafka handlers directly)
# ---------------------------------------------------------------------------


async def sync_flag_embedding(
    flag_key: str,
    name: str,
    description: str,
    tags: list[str],
    db_pool: asyncpg.Pool,
    model: SentenceTransformer,
) -> None:
    """Generate a BGE-M3 embedding for the flag and persist it to PostgreSQL.

    The text fed to the encoder is:  ``<name> <description> <tag1> <tag2> …``

    This function is intentionally synchronous-CPU-bound for the encode step
    (run_in_executor) and async for the DB write so it integrates cleanly into
    the asyncio event loop.
    """
    text = f"{name} {description} {' '.join(tags)}"
    loop = asyncio.get_event_loop()
    embedding: list[float] = await loop.run_in_executor(
        None,
        lambda: model.encode(text, normalize_embeddings=True).tolist(),
    )

    await db_pool.execute(
        "UPDATE flags SET embedding = $1::vector WHERE key = $2",
        str(embedding),
        flag_key,
    )
    logger.debug("Synced embedding for flag %s (dim=%d)", flag_key, len(embedding))


# ---------------------------------------------------------------------------
# Service class — startup backfill + event-driven sync
# ---------------------------------------------------------------------------


class EmbeddingSyncService:
    """Manages embedding lifecycle for all flags.

    Usage (from main.py lifespan):
        sync_svc = EmbeddingSyncService(db_url=os.environ["DB_URL"])
        await sync_svc.initialize()          # loads model + fires backfill
        # ... yield ...
        await sync_svc.close()

    The Kafka consumer (or webhook handler) calls:
        await sync_svc.on_flag_event(event_type, flag_key, name, description, tags)
    """

    def __init__(self, db_url: str) -> None:
        self._db_url = db_url
        self._pool: asyncpg.Pool | None = None
        self._model: SentenceTransformer | None = None

    async def initialize(self) -> None:
        """Load the BGE-M3 model and start the background backfill task."""
        self._pool = await asyncpg.create_pool(self._db_url)
        try:
            loop = asyncio.get_event_loop()
            self._model = await loop.run_in_executor(
                None, lambda: SentenceTransformer(_MODEL_NAME)
            )
            logger.info("EmbeddingSyncService: BGE-M3 model loaded")
        except Exception as exc:
            logger.warning(
                "EmbeddingSyncService: model load failed (%s) — embedding sync disabled", exc
            )
            self._model = None
            return

        asyncio.create_task(self._backfill())

    async def close(self) -> None:
        if self._pool is not None:
            await self._pool.close()

    # ------------------------------------------------------------------
    # Public event handler (called by Kafka consumer / webhook routes)
    # ------------------------------------------------------------------

    async def on_flag_event(
        self,
        event_type: str,
        flag_key: str,
        name: str,
        description: str,
        tags: list[str],
    ) -> None:
        """Handle flag.created and flag.updated events."""
        if event_type not in {"flag.created", "flag.updated"}:
            return
        if self._model is None or self._pool is None:
            return
        try:
            await sync_flag_embedding(
                flag_key=flag_key,
                name=name,
                description=description,
                tags=tags,
                db_pool=self._pool,
                model=self._model,
            )
        except Exception as exc:
            logger.warning(
                "EmbeddingSyncService: failed to sync embedding for %s (%s)", flag_key, exc
            )

    # ------------------------------------------------------------------
    # Startup backfill
    # ------------------------------------------------------------------

    async def _backfill(self) -> None:
        """Backfill embeddings for flags that have embedding IS NULL.

        Processes flags in batches of _BATCH_SIZE to avoid hammering the DB
        or CPU.  Each batch is embedded sequentially (model.encode is CPU-bound).
        """
        if self._pool is None or self._model is None:
            return

        rows = await self._pool.fetch(
            """
            SELECT key, name, description
            FROM flags
            WHERE embedding IS NULL
              AND state IN ('ACTIVE', 'DRAFT', 'COMPLETE')
            ORDER BY created_at
            """
        )

        if not rows:
            logger.info("EmbeddingSyncService: no flags need backfill")
            return

        logger.info("Backfilling embeddings: %d flags queued", len(rows))

        for batch_start in range(0, len(rows), _BATCH_SIZE):
            batch = rows[batch_start : batch_start + _BATCH_SIZE]
            for row in batch:
                try:
                    await sync_flag_embedding(
                        flag_key=row["key"],
                        name=row["name"],
                        description=row["description"],
                        tags=[],
                        db_pool=self._pool,
                        model=self._model,
                    )
                except Exception as exc:
                    logger.warning(
                        "EmbeddingSyncService: backfill failed for %s (%s)", row["key"], exc
                    )
            logger.debug(
                "EmbeddingSyncService: backfilled batch %d/%d",
                min(batch_start + _BATCH_SIZE, len(rows)),
                len(rows),
            )
