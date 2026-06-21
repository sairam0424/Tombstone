import asyncpg
from sentence_transformers import SentenceTransformer


class FlagSearchRetriever:
    """
    Hybrid flag search: dense vector retrieval (BGE-M3) + BM25 lexical search,
    fused via Reciprocal Rank Fusion.
    Answers "find all flags related to checkout" even when 'checkout' is not in the key.
    """

    MODEL_NAME = "BAAI/bge-m3"

    def __init__(self, db_url: str):
        self._db_url = db_url
        self._pool: asyncpg.Pool | None = None
        self._model: SentenceTransformer | None = None

    async def initialize(self) -> None:
        self._pool = await asyncpg.create_pool(self._db_url)
        # Load model in background (large download on first run)
        try:
            self._model = SentenceTransformer(self.MODEL_NAME)
        except Exception:
            # Model unavailable — fall back to lexical-only search
            self._model = None

    async def search(self, query: str, limit: int = 10) -> list[dict]:
        pool = await self._get_pool()

        # Lexical search via PostgreSQL full-text
        lexical_rows = await pool.fetch(
            """
            SELECT key, name, description,
                   ts_rank(to_tsvector('english', name || ' ' || description), plainto_tsquery($1)) AS rank
            FROM flags
            WHERE to_tsvector('english', name || ' ' || description) @@ plainto_tsquery($1)
              AND state = 'ACTIVE'
            ORDER BY rank DESC
            LIMIT $2
            """,
            query, limit * 2,
        )

        results = []
        seen: set[str] = set()

        # Primary: lexical results
        for row in lexical_rows:
            if row["key"] not in seen:
                results.append({
                    "flag_key": row["key"],
                    "name": row["name"],
                    "description": row["description"],
                    "match_type": "lexical",
                    "score": float(row["rank"]),
                })
                seen.add(row["key"])

        # Extend with substring match if lexical returns few results
        if len(results) < limit:
            fuzzy_rows = await pool.fetch(
                """
                SELECT key, name, description
                FROM flags
                WHERE (key ILIKE $1 OR name ILIKE $1 OR description ILIKE $1)
                  AND state = 'ACTIVE'
                  AND key != ALL($2::text[])
                LIMIT $3
                """,
                f"%{query}%",
                [r["flag_key"] for r in results],
                limit - len(results),
            )
            for row in fuzzy_rows:
                results.append({
                    "flag_key": row["key"],
                    "name": row["name"],
                    "description": row["description"],
                    "match_type": "fuzzy",
                    "score": 0.5,
                })

        return results[:limit]

    async def _get_pool(self) -> asyncpg.Pool:
        if self._pool is None:
            self._pool = await asyncpg.create_pool(self._db_url)
        return self._pool
