export const API_URL = import.meta.env.VITE_API_URL ?? 'http://localhost:8081';
export const GATEWAY_URL = import.meta.env.VITE_GATEWAY_URL ?? 'http://localhost:8080';
export const EVAL_URL = import.meta.env.VITE_EVAL_URL ?? 'http://localhost:8082';
export const INTEL_URL = import.meta.env.VITE_INTEL_URL ?? 'http://localhost:8083';
export const MARKETPLACE_URL = import.meta.env.VITE_MARKETPLACE_URL ?? 'http://localhost:8086';
export const SDK_TOKEN = import.meta.env.VITE_SDK_TOKEN ?? 'sdk-dev-token-change-in-prod';

// ── Feature availability gates ─────────────────────────────────────────────
// Set these env vars in Vercel (or .env.local) to enable services.
// Default: false — services are hidden until explicitly enabled.
export const ENABLE_INTELLIGENCE = import.meta.env.VITE_ENABLE_INTELLIGENCE === 'true';
export const ENABLE_EVALUATOR    = import.meta.env.VITE_ENABLE_EVALUATOR === 'true';
export const ENABLE_MARKETPLACE  = import.meta.env.VITE_ENABLE_MARKETPLACE === 'true';
