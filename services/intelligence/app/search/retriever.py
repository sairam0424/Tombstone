import asyncio
import logging

import asyncpg
from sentence_transformers import SentenceTransformer

logger = logging.getLogger(__name__)


class FlagSearchRetriever:
    """
    Hybrid flag search: dense vector retrieval (BGE-M3) + full-text lexical search
    + ILIKE fallback, fused via 3-way Reciprocal Rank Fusion (RRF, k=60).

    Dense path uses pgvector cosine similarity on the flags.embedding column
    (populated by EmbeddingSyncService on flag create/update, backfilled on startup).
    If the pgvector extension is unavailable at query time, the dense path is skipped
    and search degrades gracefully to lexical + ILIKE only.
    """

    MODEL_NAME = "BAAI/bge-m3"
    _RRF_K = 60

    def __init__(self, db_url: str):
        self._db_url = db_url
        self._pool: asyncpg.Pool | None = None
        self._model: SentenceTransformer | None = None

    # ------------------------------------------------------------------
    # Lifecycle
    # ------------------------------------------------------------------

    async def initialize(self) -> None:
        self._pool = await asyncpg.create_pool(self._db_url)
        try:
            self._model = SentenceTransformer(self.MODEL_NAME)
            logger.info("BGE-M3 model loaded — dense vector search enabled")
        except Exception as exc:
            logger.warning("BGE-M3 model unavailable (%s) — falling back to lexical-only search", exc)
            self._model = None

    # ------------------------------------------------------------------
    # Public API
    # ------------------------------------------------------------------

    async def search(self, query: str, limit: int = 10) -> list[dict]:
        """Return up to *limit* flags ranked by 3-way RRF fusion."""
        pool = await self._get_pool()

        # Run all three retrieval arms; dense is skipped if model absent or pgvector fails
        dense_task = self._vector_search(pool, query, limit * 2) if self._model else asyncio.coroutine(lambda: [])()
        lexical_task = self._fulltext_search(pool, query, limit * 2)
        fallback_task = self._ilike_search(pool, query, limit * 2)

        dense_results, lexical_results, fallback_results = await asyncio.gather(
            dense_task, lexical_task, fallback_task
        )

        fused = self._rrf_merge([dense_results, lexical_results, fallback_results], limit)
        return fused

    # ------------------------------------------------------------------
    # Retrieval arms
    # ------------------------------------------------------------------

    async def _vector_search(self, pool: asyncpg.Pool, query: str, limit: int) -> list[dict]:
        """Dense retrieval via pgvector cosine similarity. Falls back to [] on any error."""
        try:
            embedding = await asyncio.get_event_loop().run_in_executor(
                None, lambda: self._embed(query)
            )
            rows = await pool.fetch(
                """
                SELECT key, name, description,
                       1 - (embedding <=> $1::vector) AS similarity
                FROM flags
                WHERE state = 'ACTIVE'
                  AND embedding IS NOT NULL
                ORDER BY embedding <=> $1::vector
                LIMIT $2
                """,
                str(embedding),
                limit,
            )
            return [
                {
                    "flag_key": row["key"],
                    "name": row["name"],
                    "description": row["description"],
                    "match_type": "dense",
                    "score": float(row["similarity"]),
                }
                for row in rows
            ]
        except Exception as exc:
            logger.warning(
                "pgvector dense search failed (%s) — dense arm skipped for this query", exc
            )
            return []

    async def _fulltext_search(self, pool: asyncpg.Pool, query: str, limit: int) -> list[dict]:
        """Lexical retrieval via PostgreSQL to_tsvector / ts_rank."""
        rows = await pool.fetch(
            """
            SELECT key, name, description,
                   ts_rank(
                       to_tsvector('english', name || ' ' || description),
                       plainto_tsquery($1)
                   ) AS rank
            FROM flags
            WHERE to_tsvector('english', name || ' ' || description) @@ plainto_tsquery($1)
              AND state = 'ACTIVE'
            ORDER BY rank DESC
            LIMIT $2
            """,
            query,
            limit,
        )
        return [
            {
                "flag_key": row["key"],
                "name": row["name"],
                "description": row["description"],
                "match_type": "lexical",
                "score": float(row["rank"]),
            }
            for row in rows
        ]

    async def _ilike_search(self, pool: asyncpg.Pool, query: str, limit: int) -> list[dict]:
        """ILIKE substring fallback for exact partial matches (key names, typos)."""
        rows = await pool.fetch(
            """
            SELECT key, name, description
            FROM flags
            WHERE (key ILIKE $1 OR name ILIKE $1 OR description ILIKE $1)
              AND state = 'ACTIVE'
            LIMIT $2
            """,
            f"%{query}%",
            limit,
        )
        return [
            {
                "flag_key": row["key"],
                "name": row["name"],
                "description": row["description"],
                "match_type": "fuzzy",
                "score": 0.5,
            }
            for row in rows
        ]

    # ------------------------------------------------------------------
    # RRF fusion
    # ------------------------------------------------------------------

    def _rrf_merge(
        self,
        result_lists: list[list[dict]],
        limit: int,
        k: int = _RRF_K,
    ) -> list[dict]:
        """Reciprocal Rank Fusion: score = sum(1 / (k + rank + 1)) across all arms."""
        scores: dict[str, float] = {}
        # Keep a representative metadata dict per flag key (first occurrence wins)
        meta: dict[str, dict] = {}

        for results in result_lists:
            for rank, item in enumerate(results):
                key = item["flag_key"]
                scores[key] = scores.get(key, 0.0) + 1.0 / (k + rank + 1)
                if key not in meta:
                    meta[key] = item

        ranked_keys = sorted(scores.keys(), key=lambda x: scores[x], reverse=True)[:limit]
        return [
            {**meta[key], "rrf_score": scores[key]}
            for key in ranked_keys
        ]

    # ------------------------------------------------------------------
    # Embedding helper (synchronous — call via run_in_executor)
    # ------------------------------------------------------------------

    def _embed(self, text: str) -> list[float]:
        """Encode *text* with BGE-M3 and return a flat float list."""
        assert self._model is not None
        vector = self._model.encode(text, normalize_embeddings=True)
        return vector.tolist()

    # ------------------------------------------------------------------
    # Internal helpers
    # ------------------------------------------------------------------

    async def _get_pool(self) -> asyncpg.Pool:
        if self._pool is None:
            self._pool = await asyncpg.create_pool(self._db_url)
        return self._pool
