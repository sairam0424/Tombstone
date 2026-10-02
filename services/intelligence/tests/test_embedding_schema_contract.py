"""Contract: flag-api's schema must declare flags.embedding at the dimension the
embedding backends actually produce.

The two live in different services, so nothing else ties them together. They
drifted once: schema.sql declared vector(768) while BAAI/bge-m3 and Bedrock
Titan V2 both return 1024-dimensional vectors, so pgvector rejected every
UPDATE ("expected 768 dimensions, not 1024") and the dense arm of the hybrid
search silently returned nothing.

The SQL is parsed from the repo checkout, not from a running database, so this
needs no Postgres. It runs wherever the whole repo is checked out (CI does).
"""

from __future__ import annotations

import os
import re
from pathlib import Path

import pytest

from app.search.embedding_model import LocalEmbeddingModel, create_embedding_model
from app.search.embedding_model_bedrock import _DIMENSIONS as BEDROCK_DIMENSIONS

# The local backend loads its model lazily and exposes no dimension constant,
# so the model it defaults to is pinned here, next to the size it produces.
BGE_M3_MODEL = "BAAI/bge-m3"
BGE_M3_DIMENSIONS = 1024

_SQL_COMMENT = re.compile(r"--[^\n]*|/\*.*?\*/", re.DOTALL)
_ALTER_FLAGS = re.compile(
    r'\bALTER\s+TABLE\s+(?:IF\s+EXISTS\s+)?(?:ONLY\s+)?(?:"?public"?\s*\.\s*)?"?flags"?\s',
    re.IGNORECASE,
)
# One action of an ALTER TABLE: ADD [COLUMN] [IF NOT EXISTS] embedding vector(N)
# or ALTER [COLUMN] embedding [[SET DATA] TYPE] vector(N). It is not anchored to
# the start of the statement, so it also finds the action inside a multi-action
# ALTER TABLE flags ... , ALTER COLUMN embedding ...
_EMBEDDING_ACTION = re.compile(
    r"(?:\bADD(?:\s+COLUMN)?(?:\s+IF\s+NOT\s+EXISTS)?|\bALTER(?:\s+COLUMN)?)"
    r'\s+"?embedding"?\s+(?:(?:SET\s+DATA\s+)?TYPE\s+)?vector\s*\(\s*(\d+)\s*\)',
    re.IGNORECASE,
)
_EMBEDDING_INDEX = re.compile(
    r"\bCREATE\s+INDEX\s+(?:IF\s+NOT\s+EXISTS\s+)?idx_flags_embedding\s+ON\s+flags\s+USING\s+(\w+)",
    re.IGNORECASE,
)
_EMBEDDING_WORD = re.compile(r"\bembedding\b", re.IGNORECASE)
_VECTOR_WORD = re.compile(r"\bvector\b", re.IGNORECASE)


def _flag_api_db_dir(
    service_root: Path = Path(__file__).resolve().parents[1],
) -> Path:
    """flag-api's db directory, a sibling of the intelligence service root
    (services/intelligence -> services/flag-api/internal/db).

    The layout is fixed relative to *service_root*, so the lookup depends neither
    on the working directory pytest was started from nor on what lies above the
    service root (a pytest --basetemp inside the checkout, say)."""
    candidate = service_root.parent / "flag-api" / "internal" / "db"
    if (candidate / "schema.sql").is_file():
        return candidate
    message = "services/flag-api/internal/db not found next to services/intelligence"
    # Raise the outcome exceptions instead of calling pytest.fail/skip: the
    # mypy job (lint-python.yml) runs without pytest installed, where those
    # calls are typed Any and a function ending in one "falls off the end".
    if os.environ.get("CI"):
        raise pytest.fail.Exception(
            f"{message}; the repo layout changed and this contract is unchecked"
        )
    raise pytest.skip.Exception(
        f"{message} (standalone checkout of the intelligence service)"
    )


def _declared_dimensions(sql: str) -> list[int]:
    """Every dimension an ALTER TABLE flags statement gives flags.embedding, in
    file order. Fails on a statement that touches the column with a vector type
    this parser does not understand, instead of silently skipping it."""
    dimensions: list[int] = []
    for statement in _SQL_COMMENT.sub("", sql).split(";"):
        if not _ALTER_FLAGS.search(statement):
            continue
        found = _EMBEDDING_ACTION.findall(statement)
        if (
            not found
            and _EMBEDDING_WORD.search(statement)
            and _VECTOR_WORD.search(statement)
        ):
            raise AssertionError(
                "unrecognised flags.embedding declaration, extend "
                f"_EMBEDDING_ACTION: {' '.join(statement.split())!r}"
            )
        dimensions.extend(int(dimension) for dimension in found)
    return dimensions


def _declared_dimensions_in(sql_file: Path) -> list[int]:
    return _declared_dimensions(sql_file.read_text(encoding="utf-8"))


def _migration_files(db_dir: Path) -> list[Path]:
    return sorted(
        (db_dir / "migrations").glob("[0-9]*_*.sql"),
        key=lambda path: int(path.name.split("_", 1)[0]),
    )


def test_embedding_backends_agree_on_dimension():
    assert BEDROCK_DIMENSIONS == BGE_M3_DIMENSIONS


def test_local_backend_defaults_to_the_model_the_dimension_is_pinned_to():
    assert LocalEmbeddingModel()._model_name == BGE_M3_MODEL
    factory_model = create_embedding_model("local")
    assert isinstance(factory_model, LocalEmbeddingModel)
    assert factory_model._model_name == BGE_M3_MODEL


def test_baseline_schema_declares_the_backend_dimension():
    """Fresh installs (docker-compose init, psql < schema.sql) apply only this file."""
    schema = _flag_api_db_dir() / "schema.sql"
    declared = _declared_dimensions_in(schema)

    assert declared, f"no flags.embedding declaration found in {schema.name}"
    assert declared[-1] == BGE_M3_DIMENSIONS, (
        f"{schema.name} declares flags.embedding vector({declared[-1]}) but the "
        f"embedding backends produce {BGE_M3_DIMENSIONS}-dimensional vectors"
    )


def test_baseline_schema_indexes_embeddings_with_hnsw():
    """schema.sql builds idx_flags_embedding on the empty column. An ivfflat index
    built there has random lists and misses most true neighbours (migration 028),
    so the baseline must create an index that needs no training data."""
    schema = _flag_api_db_dir() / "schema.sql"
    sql = _SQL_COMMENT.sub("", schema.read_text(encoding="utf-8"))

    methods = [method.lower() for method in _EMBEDDING_INDEX.findall(sql)]

    assert methods == ["hnsw"], (
        f"{schema.name} creates idx_flags_embedding with {methods}, want ['hnsw']"
    )


def test_migrated_schema_declares_the_backend_dimension():
    """The last declaration wins once schema.sql and every migration have run."""
    db_dir = _flag_api_db_dir()
    files = [db_dir / "schema.sql", *_migration_files(db_dir)]
    declarations = [
        (sql_file.name, dim)
        for sql_file in files
        for dim in _declared_dimensions_in(sql_file)
    ]

    assert declarations, (
        "no flags.embedding declaration found in schema.sql or migrations"
    )
    last_file, last_dim = declarations[-1]
    assert last_dim == BGE_M3_DIMENSIONS, (
        f"after all migrations flags.embedding is vector({last_dim}) (last set by "
        f"{last_file}) but the embedding backends produce {BGE_M3_DIMENSIONS}-dimensional vectors"
    )


# --- the parser itself: the contract is only as good as what it recognises ---


@pytest.mark.parametrize(
    "statement",
    [
        "ALTER TABLE flags ALTER COLUMN embedding TYPE vector(768);",
        "ALTER TABLE flags ALTER COLUMN embedding SET DATA TYPE vector(768);",
        "ALTER TABLE flags ALTER COLUMN embedding TYPE vector(768) USING NULL;",
        "ALTER TABLE flags ALTER embedding TYPE vector(768);",
        "ALTER TABLE public.flags ALTER COLUMN embedding TYPE vector(768);",
        'ALTER TABLE "public"."flags" ALTER COLUMN "embedding" TYPE vector(768);',
        "ALTER TABLE IF EXISTS flags ALTER COLUMN embedding TYPE vector(768);",
        "ALTER TABLE ONLY flags ALTER COLUMN embedding TYPE vector(768);",
        "ALTER TABLE flags ADD COLUMN embedding vector(768);",
        "ALTER TABLE flags ADD COLUMN IF NOT EXISTS embedding vector(768);",
        "ALTER TABLE flags ADD embedding vector(768);",
        "ALTER TABLE flags ADD COLUMN tag text, ALTER COLUMN embedding TYPE vector(768);",
        "alter table flags\n  alter column embedding\n  type vector ( 768 );",
        "/* was vector(1) */ ALTER TABLE flags ALTER COLUMN embedding TYPE vector(768);",
        "DO $$ BEGIN ALTER TABLE flags ALTER COLUMN embedding TYPE vector(768); END $$;",
    ],
)
def test_parser_reads_the_dimension_from_every_spelling(statement: str):
    assert _declared_dimensions(statement) == [768]


def test_parser_returns_declarations_in_file_order():
    sql = (
        "ALTER TABLE flags ADD COLUMN IF NOT EXISTS embedding vector(768);\n"
        "ALTER TABLE flags ALTER COLUMN embedding TYPE vector(1024);\n"
    )
    assert _declared_dimensions(sql) == [768, 1024]


def test_parser_ignores_comments_and_other_tables():
    sql = (
        "-- ALTER TABLE flags ALTER COLUMN embedding TYPE vector(1);\n"
        "/* ALTER TABLE flags ALTER COLUMN embedding TYPE vector(2); */\n"
        "ALTER TABLE other ADD COLUMN embedding vector(3);\n"
        "ALTER TABLE flags ADD COLUMN name text;\n"
        "CREATE INDEX idx ON flags USING ivfflat (embedding vector_cosine_ops);\n"
    )
    assert _declared_dimensions(sql) == []


@pytest.mark.parametrize(
    "statement",
    [
        "ALTER TABLE flags ALTER COLUMN embedding TYPE vector;",
        "ALTER TABLE flags ALTER COLUMN embedding TYPE public.vector(768);",
    ],
)
def test_parser_fails_on_a_declaration_it_cannot_read(statement: str):
    with pytest.raises(AssertionError, match="unrecognised flags.embedding"):
        _declared_dimensions(statement)


def test_missing_layout_fails_under_ci_and_skips_elsewhere(
    monkeypatch: pytest.MonkeyPatch, tmp_path: Path
):
    service_root = tmp_path / "intelligence"
    monkeypatch.setenv("CI", "true")
    with pytest.raises(pytest.fail.Exception, match="layout changed"):
        _flag_api_db_dir(service_root)

    monkeypatch.delenv("CI")
    with pytest.raises(pytest.skip.Exception, match="standalone checkout"):
        _flag_api_db_dir(service_root)


def test_layout_is_found_next_to_the_service_root(tmp_path: Path):
    db_dir = tmp_path / "flag-api" / "internal" / "db"
    db_dir.mkdir(parents=True)
    (db_dir / "schema.sql").write_text("-- empty\n", encoding="utf-8")

    assert _flag_api_db_dir(tmp_path / "intelligence") == db_dir
