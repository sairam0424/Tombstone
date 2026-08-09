# Tombstone Ruby SDK

The official Ruby SDK for [Tombstone](https://github.com/sairam0424/Tombstone) — production intelligence layer for feature flags.

## Installation

Add to your Gemfile:

```ruby
gem "tombstone"
```

Then run:

```bash
bundle install
```

Or install directly:

```bash
gem install tombstone
```

## Quick Start

```ruby
require "tombstone"

client = Tombstone::Client.new(
  api_url: "http://localhost:8081",
  sdk_key: "sdk-dev-token-change-in-prod",
  environment: "development"
)
client.initialize

enabled = client.enabled?("my-first-flag", user_id: "user-123")
puts "Feature enabled: #{enabled}"
```

## Configuration

| Option | Type | Required | Default | Description |
|--------|------|----------|---------|-------------|
| `api_url` | `String` | Yes | — | Base URL of your Tombstone flag-api service |
| `sdk_key` | `String` | Yes | — | SDK authentication token |
| `environment` | `String` | No | `"production"` | Environment name |
| `cache_ttl` | `Integer` | No | `30` | Flag cache TTL in seconds |
| `timeout` | `Float` | No | `5.0` | HTTP request timeout in seconds |
| `stream_url` | `String` | No | `nil` | Gateway SSE URL for real-time updates |

```ruby
client = Tombstone::Client.new(
  api_url: "http://localhost:8081",
  sdk_key: "sdk-dev-token-change-in-prod",
  environment: "staging",
  cache_ttl: 60,
  timeout: 3.0,
  stream_url: "http://localhost:8080"
)
client.initialize
```

## Usage

### `enabled?()`

Returns `true` if the flag is enabled for the given evaluation context.

```ruby
# Simple boolean check
enabled = client.enabled?("my-first-flag", user_id: "user-123")

# With richer context for targeting rules
enabled = client.enabled?("checkout-v2",
  user_id: "user-123",
  email: "user@example.com",
  plan: "pro",
  country: "US"
)

if enabled
  show_new_checkout
else
  show_legacy_checkout
end
```

### `variation()`

Returns the variation value for multivariate flags. Use when you need string/number/hash values rather than a boolean.

```ruby
# String variation
theme = client.variation("ui-theme", { user_id: "user-123" }, default: "light")
# Returns: "light", "dark", or "high-contrast"

# Hash variation
config = client.variation("pricing-config", { user_id: "user-123" }, default: {})
# Returns: { monthly: 29, annual: 249 }

# Numeric variation
limit = client.variation("rate-limit-tier", { user_id: "user-123" }, default: 100)
```

## Rails Integration

### Initializer

Create `config/initializers/tombstone.rb`:

```ruby
Tombstone.configure do |config|
  config.api_url     = ENV.fetch("TOMBSTONE_API_URL", "http://localhost:8081")
  config.sdk_key     = ENV.fetch("TOMBSTONE_SDK_KEY")
  config.environment = Rails.env
  config.cache_ttl   = 30
end
```

### Application controller helper

```ruby
# app/controllers/application_controller.rb
class ApplicationController < ActionController::Base
  helper_method :flag_enabled?

  private

  def flag_enabled?(flag_key, context = {})
    Tombstone.client.enabled?(flag_key, { user_id: current_user&.id }.merge(context))
  end
end
```

### In controllers

```ruby
class CheckoutController < ApplicationController
  def show
    if flag_enabled?("checkout-v2")
      render "checkout_v2"
    else
      render "checkout_v1"
    end
  end
end
```

### In views

```erb
<% if flag_enabled?("new-header") %>
  <%= render "shared/header_v2" %>
<% else %>
  <%= render "shared/header_v1" %>
<% end %>
```

### With Rack middleware (for SSE streaming updates)

```ruby
# config/application.rb
config.middleware.use Tombstone::Middleware::StreamSync,
  stream_url: ENV.fetch("TOMBSTONE_STREAM_URL", "http://localhost:8080")
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

```ruby
# Local development — no changes needed from Quick Start
client = Tombstone::Client.new(
  api_url: "http://localhost:8081",
  sdk_key: "sdk-dev-token-change-in-prod",
  environment: "development"
)
```

## Testing

Use `Tombstone::TestClient` for unit and integration tests — it evaluates flags in-memory without any network calls.

```ruby
require "tombstone/testing"

RSpec.describe CheckoutController do
  let(:tombstone) { Tombstone::TestClient.new }

  before do
    allow(Tombstone).to receive(:client).and_return(tombstone)
  end

  context "when checkout-v2 is enabled" do
    before { tombstone.set_flag("checkout-v2", enabled: true) }

    it "renders the v2 checkout" do
      get :show
      expect(response).to render_template("checkout_v2")
    end
  end

  context "when checkout-v2 is disabled" do
    before { tombstone.set_flag("checkout-v2", enabled: false) }

    it "renders the legacy checkout" do
      get :show
      expect(response).to render_template("checkout_v1")
    end
  end
end
```

`Tombstone::TestClient` implements the same interface as `Tombstone::Client`, so it works with any dependency injection or stub pattern.

### Minitest

```ruby
class CheckoutTest < ActiveSupport::TestCase
  setup do
    @tombstone = Tombstone::TestClient.new
    Tombstone.stub(:client, @tombstone) do
      @tombstone.set_flag("checkout-v2", enabled: true)
    end
  end

  test "shows new checkout when flag enabled" do
    @tombstone.set_flag("checkout-v2", enabled: true)
    get checkout_path
    assert_template "checkout_v2"
  end
end
```

## Requirements

- Ruby 3.1+
- `faraday` (HTTP client)
- `concurrent-ruby` (thread-safe cache)

No other runtime dependencies. Rails is optional.
