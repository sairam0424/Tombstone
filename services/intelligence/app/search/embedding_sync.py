"""
Embedding sync utilities for the Tombstone intelligence service.

Provides:
  - sync_flag_embedding()   — update a single flag's vector in PostgreSQL
  - EmbeddingSyncService    — backfill on startup + process flag.created/flag.updated events
"""

import asyncio
import logging

import asyncpg

from app.graph.builder import DEFAULT_PROJECT_ID
from app.search.embedding_model import EmbeddingModel

logger = logging.getLogger(__name__)

_BATCH_SIZE = 50


# ---------------------------------------------------------------------------
# Low-level helper (also callable from routes / Kafka handlers directly)
# ---------------------------------------------------------------------------


async def sync_flag_embedding(
    flag_key: str,
    name: str,
    description: str,
    tags: list[str],
    db_pool: asyncpg.Pool,
    model: EmbeddingModel,
    project_id: str,
) -> None:
    """Generate an embedding for the flag and persist it to PostgreSQL.

    The text fed to the encoder is:  ``<name> <description> <tag1> <tag2> …``

    Uses the injected EmbeddingModel protocol — works with LocalEmbeddingModel
    (SentenceTransformer) or BedrockEmbeddingModel (AWS Titan V2) transparently.

    flags.key is only unique per (project_id, key) -- matching by key alone
    would update every project's identically-keyed flag's embedding in one
    call, so project_id is a required scoping parameter, not optional.
    """
    text = f"{name} {description} {' '.join(tags)}"
    vecs = await model.embed([text])
    embedding = vecs[0] if vecs and vecs[0] else None
    if embedding is None:
        logger.warning(
            "sync_flag_embedding: empty vector for flag %s — skipping update", flag_key
        )
        return

    await db_pool.execute(
        "UPDATE flags SET embedding = $1::vector WHERE key = $2 AND project_id = $3",
        str(embedding),
        flag_key,
        project_id,
    )
    logger.debug("Synced embedding for flag %s (dim=%d)", flag_key, len(embedding))


# ---------------------------------------------------------------------------
# Service class — startup backfill + event-driven sync
# ---------------------------------------------------------------------------


class EmbeddingSyncService:
    """Manages embedding lifecycle for all flags.

    Usage (from main.py lifespan):
        sync_svc = EmbeddingSyncService(db_url=os.environ["DB_URL"], embedding_model=model)
        await sync_svc.initialize()          # initializes model + fires backfill
        # ... yield ...
        await sync_svc.close()

    The Kafka consumer (or webhook handler) calls:
        await sync_svc.on_flag_event(event_type, flag_key, name, description, tags)

    Pass embedding_model=None to disable embedding sync entirely (lexical-only mode).
    """

    def __init__(
        self, db_url: str, embedding_model: EmbeddingModel | None = None
    ) -> None:
        self._db_url = db_url
        self._pool: asyncpg.Pool | None = None
        self._embedding_model: EmbeddingModel | None = embedding_model

    async def initialize(self) -> None:
        """Initialize the DB pool, the embedding model, and start the background backfill task."""
        # statement_cache_size=0: pooler-safe under DATA-2's PgBouncer
        # (transaction pooling) — see search/retriever.py's initialize()
        # for the full explanation.
        self._pool = await asyncpg.create_pool(
            self._db_url,
            min_size=1,
            max_size=3,
            max_inactive_connection_lifetime=30.0,
            statement_cache_size=0,
        )
        if self._embedding_model is not None:
            try:
                await self._embedding_model.initialize()
                logger.info("EmbeddingSyncService: embedding model initialized")
            except Exception as exc:
                logger.warning(
                    "EmbeddingSyncService: model initialization failed (%s) — embedding sync disabled",
                    exc,
                )
                self._embedding_model = None
                return
        else:
            logger.warning(
                "EmbeddingSyncService: no embedding model — embedding sync disabled"
            )
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
        project_id: str = DEFAULT_PROJECT_ID,
    ) -> None:
        """Handle flag.created and flag.updated events.

        project_id defaults to DEFAULT_PROJECT_ID because the Kafka/Redis-
        Streams event payload this is called from carries no project_id
        field today -- same deferred-scoping limitation documented on
        kafka/consumer.py's update_on_flag_change call sites.
        """
        if event_type not in {"flag.created", "flag.updated"}:
            return
        if self._embedding_model is None or self._pool is None:
            return
        try:
            await sync_flag_embedding(
                flag_key=flag_key,
                name=name,
                description=description,
                tags=tags,
                db_pool=self._pool,
                model=self._embedding_model,
                project_id=project_id,
            )
        except Exception as exc:
            logger.warning(
                "EmbeddingSyncService: failed to sync embedding for %s (%s)",
                flag_key,
                exc,
            )

    # ------------------------------------------------------------------
    # Startup backfill
    # ------------------------------------------------------------------

    async def _backfill(self) -> None:
        """Backfill embeddings for flags that have embedding IS NULL.

        Processes flags in batches of _BATCH_SIZE to avoid hammering the DB
        or CPU.  Each batch is embedded sequentially.
        """
        if self._pool is None or self._embedding_model is None:
            return

        rows = await self._pool.fetch(
            """
            SELECT key, name, description, project_id
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
                        model=self._embedding_model,
                        project_id=row["project_id"],
                    )
                except Exception as exc:
                    logger.warning(
                        "EmbeddingSyncService: backfill failed for %s (%s)",
                        row["key"],
                        exc,
                    )
            logger.debug(
                "EmbeddingSyncService: backfilled batch %d/%d",
                min(batch_start + _BATCH_SIZE, len(rows)),
                len(rows),
            )
