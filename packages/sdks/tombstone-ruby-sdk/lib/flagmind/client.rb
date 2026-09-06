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
      # Passes a real flag_lookup backed by @cache, NOT the 4-positional-arg
      # call used before -- EvaluationEngine#evaluate defaults flag_lookup to
      # ->(k) { nil } when omitted, documented there as being for callers
      # with no snapshot access. This client DOES have snapshot access via
      # @cache, but never threaded it through. Before this same PR's other
      # fix (parse_prerequisites), prerequisites was always [] and Step 2
      # never actually ran against real data, so a nil-returning lookup was
      # dead code from this call path specifically. Once prerequisites are
      # real, omitting flag_lookup here would make ANY hard-gated
      # prerequisite permanently PREREQUISITE_FAILED regardless of the real
      # dependency's state (nil never equals a real required_variation
      # string) -- swapping "prerequisites silently ignored" for "every
      # gated flag permanently blocked", which is worse. Found while
      # verifying this PR against the identical bug an adversarial review
      # found in the Java SDK's equivalent fix (PR #231).
      @engine.evaluate(state, context, default_value, flag_key, flag_lookup: ->(k) { @cache.get(k) })
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
      @cache.load_snapshot(parse_snapshot_flags(JSON.parse(resp.body)))
    rescue => e
      warn "[Tombstone] snapshot fetch failed: #{e.message}"
    end

    # Extracted so a spec can exercise the real wire-parsing logic directly
    # with a hand-built Hash (already run through JSON.parse), without
    # stubbing Net::HTTP -- mirrors client_lag_spec.rb's existing
    # `client.send(:private_method)` convention for testing private methods.
    def parse_snapshot_flags(data)
      (data["flags"] || []).map do |f|
        FlagEnvironmentState.new(
          flag_id: f["flag_id"] || "", flag_key: f["flag_key"] || "",
          environment: f["environment"] || "", enabled: f["enabled"] == true,
          rollout_pct: (f["rollout_pct"] || 0).to_i,
          safe_default: f["safe_default"] || "false",
          updated_at: (f["updated_at"] || 0).to_i,
          prerequisites: parse_prerequisites(f["prerequisites"])
        )
      end
    end

    # flag-api's real per-prerequisite wire shape (services/flag-api/
    # internal/api/v1/environments.go's SnapshotPrerequisite): "flag_key"
    # (NOT "prereq_flag_key" -- that's only flag_prerequisites' own DB
    # column name, matching proto's ParentCondition message and every other
    # SDK's own FlagPrerequisite type), plus "required_variation"/"gate".
    # "gate" defaults to true (hard-blocking) when the wire omits it,
    # matching flag-api's own AddPrerequisite default.
    #
    # Before this fix, fetch_snapshot never passed prerequisites: at all,
    # so every FlagEnvironmentState defaulted to prerequisites: [] --
    # PrerequisiteChecker.check_all's algorithm is otherwise correct, but
    # was completely unreachable with real gating data (found by
    # adversarial review of the Python SDK's equivalent fix, PR #229).
    def parse_prerequisites(raw)
      return [] unless raw.is_a?(Array)
      raw.filter_map do |p|
        next unless p.is_a?(Hash)
        FlagPrerequisite.new(
          flag_key: p["flag_key"] || "",
          required_variation: p["required_variation"] || "true",
          gate: p["gate"] != false
        )
      end
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
