# Tombstone Python SDK

The official Python SDK for [Tombstone](https://github.com/sairam0424/Tombstone) — production intelligence layer for feature flags.

## Installation

```bash
pip install tombstone
```

## Quick Start

```python
from tombstone import TombstoneClient

client = TombstoneClient(
    api_url="http://localhost:8081",
    sdk_key="sdk-dev-token-change-in-prod",
    environment="development",
)
client.initialize()

enabled = client.is_enabled("my-first-flag", {"user_id": "user-123"})
print(f"Feature enabled: {enabled}")
```

## Configuration

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `api_url` | `str` | Yes | — | Base URL of your Tombstone flag-api service |
| `sdk_key` | `str` | Yes | — | SDK authentication token (set via `FLAG_API_TOKEN` in infra/.env) |
| `environment` | `str` | No | `"production"` | Environment name (`development`, `staging`, `production`) |
| `cache_ttl` | `int` | No | `30` | Flag cache TTL in seconds |
| `timeout` | `float` | No | `5.0` | HTTP request timeout in seconds |
| `stream_url` | `str` | No | None | Gateway SSE URL for real-time updates (e.g. `http://localhost:8080`) |

```python
from tombstone import TombstoneClient

client = TombstoneClient(
    api_url="http://localhost:8081",
    sdk_key="sdk-dev-token-change-in-prod",
    environment="staging",
    cache_ttl=60,
    timeout=3.0,
    stream_url="http://localhost:8080",
)
client.initialize()
```

## Usage

### `is_enabled()`

Returns `True` if the flag is enabled for the given evaluation context.

```python
# Simple boolean flag
enabled = client.is_enabled("my-first-flag", {"user_id": "user-123"})

# With richer context for targeting rules
enabled = client.is_enabled("checkout-v2", {
    "user_id": "user-123",
    "email": "user@example.com",
    "plan": "pro",
    "country": "US",
})

if enabled:
    show_new_checkout()
else:
    show_legacy_checkout()
```

### `get_variation()`

Returns the variation value for multivariate flags. Use when you need string/number/JSON values rather than a boolean.

```python
# String variation
theme = client.get_variation("ui-theme", {"user_id": "user-123"}, default="light")
# Returns: "light", "dark", or "high-contrast"

# JSON variation
config = client.get_variation("pricing-config", {"user_id": "user-123"}, default={})
# Returns: {"monthly": 29, "annual": 249}

# Numeric variation
limit = client.get_variation("rate-limit-tier", {"user_id": "user-123"}, default=100)
```

## Async Support

The SDK ships an async client for use with `asyncio`, FastAPI, and other async frameworks.

```python
import asyncio
from tombstone import AsyncTombstoneClient

async def main():
    client = AsyncTombstoneClient(
        api_url="http://localhost:8081",
        sdk_key="sdk-dev-token-change-in-prod",
        environment="development",
    )
    await client.initialize()

    enabled = await client.is_enabled("my-first-flag", {"user_id": "user-123"})
    print(f"Feature enabled: {enabled}")

    await client.close()

asyncio.run(main())
```

### FastAPI integration

```python
from contextlib import asynccontextmanager
from fastapi import FastAPI, Request
from tombstone import AsyncTombstoneClient

client: AsyncTombstoneClient

@asynccontextmanager
async def lifespan(app: FastAPI):
    global client
    client = AsyncTombstoneClient(
        api_url="http://localhost:8081",
        sdk_key="sdk-dev-token-change-in-prod",
        environment="production",
    )
    await client.initialize()
    yield
    await client.close()

app = FastAPI(lifespan=lifespan)

@app.get("/checkout")
async def checkout(request: Request, user_id: str):
    enabled = await client.is_enabled("checkout-v2", {"user_id": user_id})
    return {"checkout_version": "v2" if enabled else "v1"}
```

## Local Development

Start the full Tombstone stack first:

```bash
git clone https://github.com/sairam0424/Tombstone.git
cd Tombstone
cp infra/.env.example infra/.env
make dev
```

The flag-api will be available at `http://localhost:8081`. The default SDK token is `sdk-dev-token-change-in-prod` (set via `FLAG_API_TOKEN` in `infra/.env`).

```python
# Local development client — no changes needed
client = TombstoneClient(
    api_url="http://localhost:8081",
    sdk_key="sdk-dev-token-change-in-prod",
    environment="development",
)
```

## Testing

Use `TombstoneTestClient` for unit tests — it evaluates flags locally without any network calls.

```python
from tombstone.testing import TombstoneTestClient

def test_checkout_page():
    client = TombstoneTestClient()
    client.set_flag("checkout-v2", enabled=True)
    client.set_variation("ui-theme", "dark")

    # Your application code under test
    result = render_checkout(client)

    assert result["checkout_version"] == "v2"
    assert result["theme"] == "dark"

def test_checkout_page_disabled():
    client = TombstoneTestClient()
    client.set_flag("checkout-v2", enabled=False)

    result = render_checkout(client)

    assert result["checkout_version"] == "v1"
```

`TombstoneTestClient` implements the same interface as `TombstoneClient`, so you can inject it via dependency injection or monkeypatching.

## Requirements

- Python 3.10+
- `httpx` (HTTP client)
- `pydantic` v2 (config validation)

No other runtime dependencies.
