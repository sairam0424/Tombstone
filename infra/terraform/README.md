# Tombstone Terraform Provider

Infrastructure-as-Code for Tombstone feature flags. The `tombstone-io/tombstone`
provider manages flags, environment bindings, and multi-region topology directly
through the Tombstone flag-api REST API.

Provider version: **0.1.x** | Compatible with Tombstone: **v2.0.0+**

## Installation

```hcl
terraform {
  required_providers {
    tombstone = {
      source  = "tombstone-io/tombstone"
      version = "~> 0.1"
    }
  }
}

provider "tombstone" {
  api_url   = "https://api.tombstone.example.com"  # or TOMBSTONE_API_URL env var
  api_token = var.tombstone_api_key                 # or TOMBSTONE_TOKEN env var
}
```

The provider reads `TOMBSTONE_API_URL` and `TOMBSTONE_TOKEN` from the environment
by default — no hard-coded credentials required.

## Resources

### `tombstone_flag`

Manages the lifecycle of a feature flag. The `key` and `flag_type` fields are
immutable after creation (ForceNew); changing either destroys and recreates the
resource.

```hcl
resource "tombstone_flag" "my_flag" {
  key          = "payments.checkout.checkout-v2"
  name         = "Checkout V2"
  description  = "Redesigned checkout flow."
  flag_type    = "BOOLEAN"    # BOOLEAN | STRING | INTEGER | FLOAT | JSON
  owner_id     = "a1b2c3d4-e5f6-7890-abcd-ef1234567890"
  safe_default = "false"      # Returned when evaluation fails
}
```

**Computed attributes:**
- `state` — Current lifecycle state: `active`, `archived`, or `tombstoned`.

**API endpoints used:**

| Terraform action | Method | Path |
|-----------------|--------|------|
| Create | POST | `/api/v1/flags` |
| Read | GET | `/api/v1/flags/{key}` |
| Update | PUT | `/api/v1/flags/{key}` |
| Delete | DELETE | `/api/v1/flags/{key}` |

---

### `tombstone_flag_environment`

Controls whether a flag is enabled and at what rollout percentage for a specific
environment. Uses PATCH semantics — delete resets the binding to disabled/0%
rather than removing the record.

```hcl
resource "tombstone_flag_environment" "my_flag_production" {
  flag_key    = tombstone_flag.my_flag.key
  environment = "production"    # development | staging | production
  enabled     = true
  rollout_pct = 25              # 0-100
}
```

The resource ID is `{flag_key}/{environment}`. Both `flag_key` and `environment`
are immutable (ForceNew).

**API endpoints used:**

| Terraform action | Method | Path |
|-----------------|--------|------|
| Create / Update | PATCH | `/api/v1/flags/{key}/environments/{env}` |
| Read | GET | `/api/v1/flags/{key}/environments/{env}` |
| Delete (reset) | PATCH | `/api/v1/flags/{key}/environments/{env}` |

---

### `tombstone_region` (NEW in v2)

Registers a region with the primary flag-api so the full multi-region topology
is tracked in Terraform state. All fields are immutable (ForceNew) — changing
`region`, `api_url`, `gateway_url`, or `is_primary` destroys and recreates the
resource.

```hcl
resource "tombstone_region" "us_east_1" {
  region      = "us-east-1"
  api_url     = "https://tombstone-us-east.example.com"
  gateway_url = "https://tombstone-gw-us-east.example.com"
  is_primary  = true
}

resource "tombstone_region" "eu_west_1" {
  region      = "eu-west-1"
  api_url     = "https://tombstone-eu-west.example.com"
  gateway_url = "https://tombstone-gw-eu-west.example.com"
  is_primary  = false
}
```

**Computed attributes:**
- `created_at` — RFC3339 timestamp of registration.
- `status` — Current health status as reported by the API.

**API endpoints used:**

| Terraform action | Method | Path |
|-----------------|--------|------|
| Create | POST | `/api/v1/regions` |
| Read | GET | `/api/v1/regions/{region}` |
| Delete | DELETE | `/api/v1/regions/{region}` |

There is no update path for regions. Flags can reference a region via
`depends_on = [tombstone_region.us_east_1]` to ensure the topology is
registered before flag creation.

---

## Data Sources

### `tombstone_flags`

Lists all flags belonging to a project. Useful for inventorying existing flags
or validating that expected flags exist before running downstream automation.

```hcl
data "tombstone_flags" "all" {
  project_id = "00000000-0000-0000-0000-000000000001"  # optional, defaults to root project
}

output "flag_count" {
  value = length(data.tombstone_flags.all.flags)
}

# Each entry in .flags has: key, name, state
output "flag_keys" {
  value = [for f in data.tombstone_flags.all.flags : f.key]
}
```

---

## Examples

### Basic single-region setup

See [`examples/basic/main.tf`](examples/basic/main.tf) — creates a flag, binds
it to development (100%) and production (0%), and demonstrates the data source.

### Multi-region topology

See [`examples/multi-region/main.tf`](examples/multi-region/main.tf) — registers
a primary (us-east-1) and secondary (eu-west-1) region, then creates a flag that
spans both:

```hcl
resource "tombstone_region" "us_east_1" {
  region      = "us-east-1"
  api_url     = "https://tombstone-us-east.example.com"
  gateway_url = "https://tombstone-gw-us-east.example.com"
  is_primary  = true
}

resource "tombstone_flag" "checkout_v2" {
  key       = "payments.checkout.checkout-v2"
  flag_type = "BOOLEAN"
  owner_id  = "a1b2c3d4-e5f6-7890-abcd-ef1234567890"

  depends_on = [tombstone_region.us_east_1]
}
```

For the full multi-region Helm deployment guide, see
[`infra/docs/multi-region.md`](../docs/multi-region.md).

## Provider Source

The provider source lives in `infra/terraform/provider/`. It is built with
the [Terraform Plugin SDK v2](https://github.com/hashicorp/terraform-plugin-sdk).

```bash
cd infra/terraform/provider
go build ./...
go test ./...
```
