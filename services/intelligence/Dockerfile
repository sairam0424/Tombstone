FROM python:3.14-slim
WORKDIR /app

# INFRA-1: HF_HOME set explicitly and BEFORE the model-bake step below, so
# the bake (which runs as root during build) and every runtime lookup
# (which may run as an arbitrary non-root UID under Kubernetes'
# runAsUser, which overrides whatever USER this Dockerfile sets) resolve
# the SAME cache path regardless of $HOME -- sentence-transformers/HF's
# default cache location is derived from $HOME, which Docker sets to "/"
# for an unmapped non-root UID with no /etc/passwd entry, and "/" is not
# writable or even listable by that UID (found by adversarial review of
# the Helm chart's securityContext hardening: this exact image, run as
# UID 65532, could not read /root or create a cache dir anywhere under /).
ENV HF_HOME=/app/.cache/huggingface

# Install uv for fast dependency resolution
RUN pip install uv

# Copy source first so editable install (-e .) works correctly
COPY pyproject.toml ./
COPY app/ app/

# Install all dependencies (including sentence-transformers). pyproject.toml's
# [tool.uv.pip] torch-backend = "cpu" selects CPU-only torch (no nvidia-*/triton).
# --no-cache keeps uv's unpacked wheel cache out of the image layer.
# The second step fails the build if CUDA wheels ever sneak back in.
RUN uv pip install --system --no-cache . \
    && python3 -c "import importlib.metadata as m; v = m.version('torch'); bad = sorted((d.metadata['Name'] or '') for d in m.distributions() if (d.metadata['Name'] or '').lower().startswith(('nvidia-', 'triton', 'cuda-'))); assert v.endswith('+cpu') and not bad, (v, bad)"

# Pre-download BAAI/bge-m3 embedding model (~2.3GB: pytorch_model.bin is 2271MB) at BUILD time.
# This bakes the model into the Docker image layer so container startup is fast.
# The download only happens once — subsequent `make dev` runs use the cached layer.
# Without this, the first startup would silently hang for several minutes.
#
# INFRA-1: also creates /app/signals here (the Argos rule-generation
# endpoint's default write target, see app/main.py's SIGNALS_DIR) and
# chmods both this and the HF cache world-readable/writable (a+rwX) --
# the runtime UID under Kubernetes is arbitrary (whatever runAsUser sets,
# independent of any USER directive below), so this can't be scoped to
# one specific UID/GID the way a normal chown would. The HF cache only
# ever needs to be READ at runtime (the model is already downloaded), so
# it stays readable even under a read-only root filesystem; /app/signals
# needs a real WRITABLE volume once the root filesystem is read-only --
# infra/helm/flagmind's ConfigMap points SIGNALS_DIR at the /tmp emptyDir
# every Deployment mounts instead of this baked-in (but now-read-only at
# runtime) path, so this chmod is defense-in-depth for plain `docker run`/
# `make dev`, not the real production write target.
RUN python3 -c "from sentence_transformers import SentenceTransformer; SentenceTransformer('BAAI/bge-m3'); print('Model cached.')" \
    && mkdir -p /app/signals \
    && chmod -R a+rwX /app/.cache /app/signals

# Kubernetes' runAsUser (set in infra/helm/flagmind/values.yaml) overrides
# this at deploy time regardless -- set for plain `docker run`/local
# non-root testing, matching the same UID the Helm chart uses.
USER 65532:65532

EXPOSE 8083
# --workers 1 is load-bearing for OBS-1's RED metrics (app/observability/
# metrics.py): the Counter/Histogram are built once against
# prometheus_client's process-wide default registry, with NO multiprocess
# mode configured (no PROMETHEUS_MULTIPROC_DIR / MultiProcessCollector).
# Raising this would silently split each worker's own request counts into
# its own registry, so a /metrics scrape would only ever see ~1/N of real
# traffic depending on which worker answered it — with no error or warning.
# If this ever needs to scale to multiple workers, add prometheus_client's
# multiprocess support first.
CMD ["uvicorn", "app.main:app", "--host", "0.0.0.0", "--port", "8083", "--workers", "1"]
