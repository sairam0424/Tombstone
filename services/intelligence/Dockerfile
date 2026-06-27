FROM python:3.12-slim
WORKDIR /app

# Install uv for fast dependency resolution
RUN pip install uv

# Copy source first so editable install (-e .) works correctly
COPY pyproject.toml ./
COPY app/ app/

# Install all dependencies (including sentence-transformers — large, cached layer)
RUN uv pip install --system .

# Pre-download BAAI/bge-m3 embedding model (~400MB) at BUILD time.
# This bakes the model into the Docker image layer so container startup is fast.
# The download only happens once — subsequent `make dev` runs use the cached layer.
# Without this, the first startup would silently hang for several minutes.
RUN python3 -c "from sentence_transformers import SentenceTransformer; SentenceTransformer('BAAI/bge-m3'); print('Model cached.')"

EXPOSE 8083
CMD ["uvicorn", "app.main:app", "--host", "0.0.0.0", "--port", "8083", "--workers", "1"]
