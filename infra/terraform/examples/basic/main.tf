terraform {
  required_providers {
    flagmind = {
      source  = "flagmind/flagmind"
      version = "~> 0.1"
    }
  }
}

provider "flagmind" {
  # Reads TOMBSTONE_API_URL and TOMBSTONE_TOKEN from the environment by default.
  # api_url   = "https://api.flagmind.io"
  # api_token = "secret"
}

# ─── Feature Flag ─────────────────────────────────────────────────────────────

resource "tombstone_flag" "checkout_v2" {
  key          = "checkout-v2"
  name         = "Checkout V2"
  description  = "Rolls out the redesigned checkout flow to customers."
  flag_type    = "BOOLEAN"
  owner_id     = "a1b2c3d4-e5f6-7890-abcd-ef1234567890"
  safe_default = "false"
}

# ─── Environment Bindings ─────────────────────────────────────────────────────

resource "tombstone_flag_environment" "checkout_v2_production" {
  flag_key    = tombstone_flag.checkout_v2.key
  environment = "production"
  enabled     = false
  rollout_pct = 0
}

resource "tombstone_flag_environment" "checkout_v2_development" {
  flag_key    = tombstone_flag.checkout_v2.key
  environment = "development"
  enabled     = true
  rollout_pct = 100
}

# ─── Data Source ──────────────────────────────────────────────────────────────

data "tombstone_flags" "all" {
  project_id = "00000000-0000-0000-0000-000000000001"

  # Ensure the flag above exists before listing (avoids race on first apply).
  depends_on = [tombstone_flag.checkout_v2]
}

# ─── Outputs ──────────────────────────────────────────────────────────────────

output "flag_state" {
  description = "Lifecycle state of the checkout_v2 flag."
  value       = tombstone_flag.checkout_v2.state
}

output "total_flags" {
  description = "Total number of flags in the project."
  value       = length(data.tombstone_flags.all.flags)
}
