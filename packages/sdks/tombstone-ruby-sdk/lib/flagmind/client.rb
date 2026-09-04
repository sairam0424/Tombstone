require "net/http"
require "uri"
require "json"
require_relative "flag_cache"
require_relative "evaluation_engine"

module Tombstone
  class Client
    def initialize(sdk_key:, environment: "production", api_url: nil, gateway_url: nil, defaults: {}, lag_debounce: 0.5)
      @sdk_key = sdk_key
      @environment = environment
      @api_url = api_url || "http://localhost:8081"
      @gateway_url = gateway_url || "http://localhost:8080"
      @defaults = defaults
      @cache = FlagCache.new
      @engine = EvaluationEngine.new
      @connected = false
      @sse_thread = nil
      @lag_debounce = lag_debounce
      @lag_mutex = Mutex.new
      @lag_timer = nil
    end

    def connect
      fetch_snapshot
      start_sse_listener
      @connected = true
    end

    def evaluate(flag_key, context)
      state = @cache.get(flag_key)
      default_value = @defaults.fetch(flag_key, false)
      @engine.evaluate(state, context, default_value, flag_key)
    end

    def enabled?(flag_key, context)
      evaluate(flag_key, context).value == true
    end

    def connected? = @connected
    def flag_keys = @cache.flag_keys

    def disconnect
      @connected = false
      @sse_thread&.kill
      @lag_timer&.kill
    end

    private

    def fetch_snapshot
      uri = URI("#{@api_url}/api/v1/environments/snapshot?environment=#{@environment}")
      req = Net::HTTP::Get.new(uri)
      req["Authorization"] = "Bearer #{@sdk_key}"
      resp = Net::HTTP.start(uri.host, uri.port) { |h| h.request(req) }
      return unless resp.is_a?(Net::HTTPSuccess)
      data = JSON.parse(resp.body)
      flags = (data["flags"] || []).map do |f|
        FlagEnvironmentState.new(
          flag_id: f["flag_id"] || "", flag_key: f["flag_key"] || "",
          environment: f["environment"] || "", enabled: f["enabled"] == true,
          rollout_pct: (f["rollout_pct"] || 0).to_i,
          safe_default: f["safe_default"] || "false", updated_at: 0
        )
      end
      @cache.load_snapshot(flags)
    rescue => e
      warn "[Tombstone] snapshot fetch failed: #{e.message}"
    end

    def start_sse_listener
      @sse_thread = Thread.new do
        while @connected
          begin
            uri = URI("#{@gateway_url}/api/v1/stream?environment=#{@environment}")
            Net::HTTP.start(uri.host, uri.port, read_timeout: 300) do |http|
              req = Net::HTTP::Get.new(uri)
              req["Authorization"] = "Bearer #{@sdk_key}"
              req["Accept"] = "text/event-stream"
              http.request(req) do |resp|
                current_event = nil
                resp.read_body do |chunk|
                  chunk.each_line do |line|
                    if line.start_with?("event:")
                      current_event = line[6..].strip
                    elsif line.start_with?("data:")
                      dispatch_sse_event(current_event, line[5..].strip)
                      current_event = nil
                    end
                  end
                end
              end
            end
          rescue => e
            sleep 3 if @connected
          end
        end
      end
      @sse_thread.abort_on_exception = false
    end

    def apply_event(json)
      data = JSON.parse(json)
      @cache.apply_event(
        data["flag_key"], data["enabled"] == true,
        (data["rollout_pct"] || 0).to_i, (data["ts"] || 0).to_i
      )
    rescue JSON::ParserError
      # malformed event — ignore
    end

    # Route a parsed SSE frame. A "lag" frame is the gateway warning us that our
    # buffer overflowed and it DROPPED the real flag-update event; recover the
    # dropped update by refetching the full snapshot. Everything else is a normal
    # flag-update event applied incrementally to the cache.
    def dispatch_sse_event(event_type, data)
      if event_type == "lag"
        schedule_snapshot_refetch
      else
        apply_event(data)
      end
    end

    # Debounced full-snapshot refetch reusing the same path connect() uses to
    # populate the cache. A slow client can receive many lag frames in a burst,
    # so coalesce them into a single refetch inside a ~@lag_debounce window. The
    # timer is a plain Thread guarded by @lag_mutex and is cancelled on disconnect.
    def schedule_snapshot_refetch
      @lag_mutex.synchronize do
        return if @lag_timer&.alive?
        @lag_timer = Thread.new do
          sleep(@lag_debounce)
          fetch_snapshot if @connected
          @lag_mutex.synchronize { @lag_timer = nil }
        end
      end
    end
  end
end
