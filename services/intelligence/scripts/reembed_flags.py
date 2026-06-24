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
