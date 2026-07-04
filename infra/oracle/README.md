# Oracle Cloud Deployment — v1.1+ Only

> **For Tombstone v1.0 self-hosted local development, use `make dev` from the repo root instead. See [README.md](../../README.md).**

This directory contains Oracle Cloud ARM VM deployment files for v1.1+ production deployments.

**Warning:** `docker-compose.prod.yml` defaults `EMBEDDING_BACKEND=bedrock` — set `EMBEDDING_BACKEND=local` if you do not have AWS Bedrock credentials.

## Files

- `docker-compose.prod.yml` — Production compose for Oracle ARM VM (5 Go services)
- `nginx.conf` — Reverse proxy config
- `cloud-init.yml` — Oracle ARM Ubuntu bootstrap script
- `setup.sh` — One-shot server setup
