terraform {
  required_providers {
    tombstone = {
      source  = "tombstone-io/tombstone"
      version = "~> 0.1"
    }
  }
}

provider "tombstone" {
  api_url   = "https://tombstone-primary.example.com"
  api_token = var.tombstone_api_key
}

# ─── Variables ────────────────────────────────────────────────────────────────

variable "tombstone_api_key" {
  description = "API key for the Tombstone provider. Read from TOMBSTONE_TOKEN env var by default."
  type        = string
  sensitive   = true
  default     = null
}

# ─── Region Topology ──────────────────────────────────────────────────────────

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

# ─── Feature Flag (global — evaluated in every region) ────────────────────────

resource "tombstone_flag" "checkout_v2" {
  key          = "payments.checkout.checkout-v2"
  name         = "Checkout V2"
  description  = "Redesigned checkout flow — multi-region rollout."
  flag_type    = "BOOLEAN"
  owner_id     = "a1b2c3d4-e5f6-7890-abcd-ef1234567890"
  safe_default = "false"

  depends_on = [tombstone_region.us_east_1]
}

# ─── Outputs ──────────────────────────────────────────────────────────────────

output "primary_region_status" {
  description = "Health status of the primary region as reported by the Tombstone API."
  value       = tombstone_region.us_east_1.status
}

output "secondary_region_status" {
  description = "Health status of the eu-west-1 secondary region."
  value       = tombstone_region.eu_west_1.status
}

output "checkout_v2_flag_state" {
  description = "Lifecycle state of the checkout_v2 flag."
  value       = tombstone_flag.checkout_v2.state
}
