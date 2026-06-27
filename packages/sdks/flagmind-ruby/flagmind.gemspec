Gem::Specification.new do |s|
  s.name        = "flagmind-ruby"
  s.version     = "0.1.0"
  s.summary     = "Tombstone Ruby SDK — server-side feature flag evaluation"
  s.description = "Ruby SDK for Tombstone, the self-hosted production intelligence layer for feature flags."
  s.authors     = ["Tombstone"]
  s.email       = "support@tombstone.dev"
  s.homepage    = "https://github.com/sairam0424/Tombstone"
  s.license     = "MIT"
  s.files       = Dir["lib/**/*.rb"]
  s.require_paths = ["lib"]
  s.required_ruby_version = ">= 3.3"
  s.add_dependency "murmurhash3", "~> 0.1"
  s.add_dependency "net-http", ">= 0.4"
  s.add_development_dependency "rspec", "~> 3.13"
end
