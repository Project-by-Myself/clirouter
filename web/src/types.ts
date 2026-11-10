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

export type BudgetPeriod = "daily" | "weekly" | "monthly" | "total";
export const BUDGET_PERIODS: BudgetPeriod[] = ["daily", "weekly", "monthly", "total"];

export interface ClientKey {
  name: string;
  key: string;
  note?: string;
  models?: string[];
  budget_usd?: number;
  budget_tokens?: number;
  budget_period?: BudgetPeriod;
  rpm?: number;
  tpm?: number;
  max_parallel?: number;
  expires_at?: string;
  disabled?: boolean;
  created_at?: string;
}

/** Fields accepted by POST/PATCH /admin/api/keys. */
export type KeyPatch = Partial<Omit<ClientKey, "key" | "created_at">> & { expires_in?: string };

/** A key as returned by the key management API (key only after create/rotate). */
export interface KeyView extends Omit<ClientKey, "key"> {
  key?: string;
  key_hint: string;
  status: string;
}

export interface Price {
  input: number;
  output: number;
  cache_read?: number;
  cache_write?: number;
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
  budget_usd?: number;
  budget_period?: BudgetPeriod;
  region?: string; // bedrock: AWS region; vertex: Google Cloud location
  project?: string; // vertex: Google Cloud project override
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
  public_url?: string;
  pricing?: Record<string, Price>;
  alerts: Alerts;
  pricing_sync: { auto?: boolean; url?: string };
}

export interface Alerts {
  webhook_url?: string;
  telegram_bot_token?: string;
  telegram_chat_id?: string;
  thresholds?: number[];
  expiry_days?: number;
  quota_pct?: number;
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
  quota?: QuotaWindow[];
  quota_at?: string;
  quota_error?: string;
}

export interface QuotaWindow {
  name: string;
  used_pct?: number;
  remaining?: number;
  limit?: number;
  resets_at?: string;
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
  cost_usd?: number;
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
  cost_usd?: number;
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
  "azure-openai": "",
  ollama: "http://127.0.0.1:11434/v1",
  bedrock: "",
};

/** Types that work without any key (one keyless account). */
export const KEYLESS_TYPES = new Set(["ollama"]);
/** Types with a region/location field. */
export const REGION_TYPES = new Set(["bedrock", "vertex"]);
/** Types with a per-type base URL / key hint in the dictionary. */
export const HINT_TYPES = new Set(["azure-openai", "bedrock", "ollama", "vertex"]);

/** Bedrock region from a bedrock-runtime[-fips].<region>.amazonaws.com URL. */
export function bedrockRegion(baseUrl: string): string {
  const m = /^https?:\/\/bedrock-runtime(?:-fips)?\.([a-z0-9-]+)\.amazonaws\.com/i.exec(baseUrl.trim());
  return m ? m[1].toLowerCase() : "";
}

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

export interface UsageTotals {
  requests: number;
  input_tokens: number;
  output_tokens: number;
  cost_usd: number;
}

export type KeyStatus = "active" | "disabled" | "expired" | "over_budget";

export interface KeyUsage extends UsageTotals {
  name: string;
  status: KeyStatus;
  period: BudgetPeriod;
  period_start: string;
  period_end?: string;
  today: UsageTotals;
  inflight: number;
  rpm_now: number;
  tpm_now?: number;
  last_used?: string;
}

export interface ProviderUsage extends UsageTotals {
  id: string;
  period: BudgetPeriod;
  period_start: string;
  period_end?: string;
  over_budget: boolean;
}

export interface UsageRow {
  key: string;
  requests: number;
  errors: number;
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
  cost_usd: number;
}

export interface UsageDay extends UsageTotals {
  day: string;
}

export interface UsageReport {
  since: string;
  total: UsageRow;
  rows: UsageRow[];
  daily: UsageDay[];
}

export interface PricingInfo {
  builtin: Record<string, Price>;
  overrides: Record<string, Price>;
  synced?: { source: string; updated_at: string; count: number };
}

export interface KeyInfo {
  name: string;
  note?: string;
  status: KeyStatus;
  models: string[];
  expires_at?: string;
  limits: { budget_usd: number; budget_tokens: number; budget_period: BudgetPeriod; rpm: number; tpm?: number; max_parallel: number };
  usage: UsageTotals & { period_start: string; period_end?: string };
  today: UsageTotals;
}
