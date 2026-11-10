export interface Metrics {
  inflight: number;
  total: number;
  errors: number;
  avg_ttfb_ms: number;
  input_tokens: number;
  output_tokens: number;
}

export interface Status {
  version: string;
  uptime_s: number;
  listen: string;
  config_path: string;
  metrics: Metrics;
  dropped_records: number;
  providers: number;
  accounts: number;
  accounts_cooling: number;
  auth_required: boolean;
}

export interface ClientKey {
  name: string;
  key: string;
}

export interface Timeouts {
  connect: number;
  first_byte: number;
  idle: number;
  total: number;
  early_ping: number;
  keepalive: number;
}

export interface Routing {
  strategy: string;
  max_attempts: number;
  session_affinity: boolean;
  max_body_mb: number;
}

export interface Logging {
  level: string;
  retention_days: number;
  capture_bodies: boolean;
}

export interface Provider {
  id: string;
  type: string;
  base_url: string;
  keys: string[];
  oauth: string[];
  models: string[];
  model_map: Record<string, string>;
  headers: Record<string, string>;
  proxy: string;
  priority: number;
  disabled: boolean;
  first_byte: number;
  idle: number;
}

export interface Combo {
  name: string;
  strategy: string;
  targets: string[];
}

export interface Config {
  listen: string;
  api_keys: ClientKey[];
  admin_password?: string;
  data_dir: string;
  timeouts: Timeouts;
  routing: Routing;
  logging: Logging;
  providers: Provider[];
  combos: Combo[];
  aliases: Record<string, string>;
}

export interface AccountStatus {
  id: string;
  provider: string;
  label: string;
  kind: string;
  inflight: number;
  cooldown_until?: string;
  model_cooldown?: Record<string, string>;
  successes: number;
  failures: number;
  last_error?: string;
  last_error_at?: string;
  last_used?: string;
  meta?: Record<string, string>;
}

export interface RecordT {
  id: number;
  ts: string;
  client_key: string;
  client_format: string;
  model: string;
  provider: string;
  account: string;
  upstream_model: string;
  status: number;
  stream: boolean;
  passthrough: boolean;
  attempts: number;
  ttfb_ms: number;
  duration_ms: number;
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
  reasoning_tokens: number;
  error?: string;
}

export interface Bucket {
  key: string;
  requests: number;
  errors: number;
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  avg_ttfb_ms: number;
  p95_ttfb_ms: number;
}

export interface Stats {
  since: string;
  total: Bucket;
  by_model: Bucket[];
  by_provider: Bucket[];
  by_key: Bucket[];
  timeline: Bucket[];
}

export interface ModelInfo {
  id: string;
  owner: string;
}

export const PROVIDER_TYPES: Record<string, string> = {
  anthropic: "https://api.anthropic.com",
  openai: "https://api.openai.com/v1",
  "openai-compatible": "",
  gemini: "https://generativelanguage.googleapis.com",
  vertex: "https://aiplatform.googleapis.com",
  codex: "https://chatgpt.com/backend-api/codex",
  "claude-oauth": "https://api.anthropic.com",
  copilot: "https://api.githubcopilot.com",
  "gemini-cli": "https://cloudcode-pa.googleapis.com",
};

export const OAUTH_TYPES = new Set(["codex", "claude-oauth", "copilot", "gemini-cli"]);

export interface OAuthStart {
  flow_id: string;
  type: string;
  mode: "redirect" | "paste_code" | "device";
  auth_url?: string;
  user_code?: string;
  verification_uri?: string;
  expires_at?: string;
}

export interface OAuthAccount {
  account_id: string;
  type: string;
  email?: string;
  plan?: string;
  project_id?: string;
  expires_at?: string;
  last_refresh?: string;
  last_error?: string;
}

export interface OAuthFlowStatus {
  state: "pending" | "done" | "error";
  type?: string;
  account_id?: string;
  error?: string;
  account?: OAuthAccount;
}

export const OAUTH_TYPE_MAP: Record<string, string> = {
  codex: "codex",
  "claude-oauth": "claude",
  copilot: "copilot",
  "gemini-cli": "gemini-cli",
};

export const OAUTH_PROVIDER_TYPE: Record<string, string> = {
  codex: "codex",
  claude: "claude-oauth",
  copilot: "copilot",
  "gemini-cli": "gemini-cli",
};
