FROM python:3.12-slim
WORKDIR /app

# Install uv for fast dependency resolution
RUN pip install uv

# Copy source first so editable install (-e .) works correctly
COPY pyproject.toml ./
COPY app/ app/

# Install all dependencies (including sentence-transformers — large, cached layer)
RUN uv pip install --system .

EXPOSE 8083
CMD ["uvicorn", "app.main:app", "--host", "0.0.0.0", "--port", "8083", "--workers", "1"]
