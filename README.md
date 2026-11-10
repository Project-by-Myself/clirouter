# clirouter

Gateway LLM tốc độ cao cho coding agent (Claude Code, Codex CLI, Cursor, Cline, Gemini CLI…).
Một endpoint duy nhất, định tuyến tới Anthropic, OpenAI, Gemini/Vertex, mọi provider
OpenAI-compatible, và tài khoản subscription qua OAuth. Một binary Go (~13 MB, ~25 MB RAM),
dashboard nhúng sẵn.

## Chạy nhanh

```bash
make build            # build dashboard + binary (cần Node 20+ và Go 1.27+)
./bin/clirouter       # lần đầu tự tạo ~/.clirouter/config.yaml
```

Mở dashboard tại http://127.0.0.1:8317 → **Providers** → thêm provider và key.

## Kết nối client

| Client | Cấu hình |
|---|---|
| Claude Code | `ANTHROPIC_BASE_URL=http://127.0.0.1:8317` · `ANTHROPIC_AUTH_TOKEN=<client key>` |
| Codex CLI | `~/.codex/config.toml`: `[model_providers.clirouter]` `base_url = "http://127.0.0.1:8317/v1"`, `wire_api = "responses"`, `env_key = "CLIROUTER_API_KEY"` |
| Cursor / Cline / opencode | OpenAI base URL `http://127.0.0.1:8317/v1` |
| Gemini CLI | `GOOGLE_GEMINI_BASE_URL=http://127.0.0.1:8317` |

Trang **Kết nối** trong dashboard sinh sẵn snippet theo cấu hình hiện tại.

## Endpoint

`POST /v1/messages` (+`/count_tokens`) · `POST /v1/chat/completions` · `POST /v1/responses` ·
`POST /v1beta/models/{model}:streamGenerateContent|generateContent` · `GET /v1/models` ·
`GET /healthz` · `GET /metrics` (Prometheus) · `/admin/api/*` (dashboard).

## Chọn model

Theo thứ tự: **alias** → **combo** → `provider/model` → provider khai báo model đó trong
`models`/`model_map` → provider không khai báo model nào nhưng cùng họ (`claude*`, `gpt*/o*`, `gemini*`).

```yaml
providers:
  - id: anthropic
    type: anthropic
    keys: [env:ANTHROPIC_API_KEY, env:ANTHROPIC_API_KEY_2]   # nhiều key = xoay vòng
  - id: openrouter
    type: openai-compatible
    base_url: https://openrouter.ai/api/v1
    keys: [env:OPENROUTER_API_KEY]
    models: [z-ai/glm-4.6]
    priority: 1                    # số nhỏ được thử trước
combos:
  - name: coding
    strategy: fallback             # hoặc round-robin
    targets: [anthropic/claude-sonnet-4-5, openrouter/z-ai/glm-4.6]
aliases:
  claude-sonnet-4-5-20250929: coding   # Claude Code gọi model này → đi qua combo
```

Loại provider: `anthropic`, `openai`, `openai-compatible`, `gemini`, `vertex`, `codex`,
`claude-oauth`, `copilot`, `gemini-cli`. Key dạng `env:NAME` đọc từ biến môi trường.
Config được theo dõi và nạp lại nóng (2 s), không cần restart (trừ `listen`).

## Thiết kế cho ổn định và tốc độ

- **Data plane tách khỏi dashboard.** Request suy luận không đi qua framework/middleware nào.
- **Passthrough byte-nguyên-vẹn** khi client và provider cùng định dạng: không parse JSON từng
  chunk, chỉ đọc usage ở event có chứa nó. Khác định dạng thì dịch qua IR có kiểu, encode thẳng
  sang định dạng client (0 alloc mỗi text chunk).
- **Không I/O đồng bộ trên hot path.** Trạng thái định tuyến nằm trong RAM, khoá theo từng
  account. Log request đi qua kênh có giới hạn tới một writer SQLite ghi theo lô; kênh đầy thì
  bỏ và đếm, không bao giờ chặn request.
- **Timeout tách bạch:** `connect` · `first_byte` (tới byte dữ liệu đầu tiên) · `idle` (im lặng
  giữa các chunk, re-arm mỗi lần nhận byte) · `total`. Client ngắt kết nối thì hủy upstream ngay.
- **Keepalive:** sau `early_ping` gửi header 200 + ping đúng chuẩn từng định dạng
  (`event: ping` cho Anthropic, comment SSE cho OpenAI), lặp lại mỗi `keepalive` khi im lặng.
- **Failover trước nội dung đầu tiên:** lỗi HTTP, timeout, hoặc lỗi in-band ngay đầu stream
  (ví dụ `overloaded_error` sau 200) đều chuyển account/provider khác mà client không thấy.
  Sau khi đã gửi nội dung, lỗi được báo in-band đúng định dạng client.
- **Cooldown thông minh:** 429 tôn trọng `Retry-After`/header quota, khoá theo model; hết quota
  khoá cả account; 400 không khoá; một lỗi 5xx lẻ không loại account. Cooldown ngắn (≤5 s) thì
  chờ thay vì trả 429.
- **Session affinity:** các lượt cùng hội thoại dính một account để giữ prompt cache.
- **Pool kết nối HTTP/2** dùng chung theo proxy, tắt gzip upstream (gzip làm SSE bị dồn cục).
- **Credential OAuth mã hoá AES-256-GCM**, ghi file nguyên tử, refresh single-flight + chạy nền.

## Không làm (có chủ đích)

MITM / sửa `/etc/hosts` / cài root CA · giả mạo header, TLS fingerprint hay system prompt của
client chính chủ · provider scrape web · gửi request sang LLM bên thứ ba để định tuyến ·
cộng khống token usage.

## Giới hạn đã biết

- **Tài khoản subscription (OAuth)**: luồng đăng nhập Codex/Claude/Copilot/Gemini CLI đã có
  nhưng **chưa kiểm chứng với server thật**. Vì không giả danh client chính chủ, provider có thể
  từ chối (Anthropic có thể chỉ chấp nhận token subscription từ Claude Code; backend Codex có thể
  yêu cầu `instructions` chính thức; Copilot có thể từ chối `Editor-Version` khác VS Code).
  Dùng subscription qua proxy có thể vi phạm điều khoản của provider — tự cân nhắc.
- Chưa hỗ trợ Cursor, Kiro, Codex WebSocket, Vertex service account.

## Phát triển

```bash
make test     # go vet + go test -race ./...
make bench    # benchmark translator, SSE, overhead proxy
cd web && npm run dev   # dashboard dev server, proxy tới :8317
```

Cấu trúc: `internal/proxy` (data plane) · `internal/router` (định tuyến, cooldown, affinity) ·
`internal/translate/*` (codec từng định dạng) · `internal/ir` · `internal/sse` ·
`internal/upstream` (HTTP client, phân loại lỗi) · `internal/auth` (OAuth) ·
`internal/store` (SQLite) · `internal/server` (route, admin API) · `web/` (dashboard React).

## License

[GPL-3.0](LICENSE) © clirouter contributors.
