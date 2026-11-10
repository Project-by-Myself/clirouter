package auth

// Endpoints and public OAuth client identifiers. They are variables so tests
// can point them at httptest servers. The client IDs are the public ones the
// official open-source CLIs use for their login flow; they are used only to
// obtain the user's own tokens. All later traffic identifies as clirouter.
var (
	// Codex / ChatGPT. The client id only allows this exact loopback redirect.
	CodexAuthorizeURL = "https://auth.openai.com/oauth/authorize"
	CodexTokenURL     = "https://auth.openai.com/oauth/token"
	CodexClientID     = "app_EMoamEEZ73f0CkXaXp7hrann"
	CodexRedirectURI  = "http://localhost:1455/auth/callback"
	CodexCallbackAddr = "127.0.0.1:1455"
	CodexScope        = "openid profile email offline_access"

	// Claude subscription. Unverified (public knowledge); may change without notice.
	ClaudeAuthorizeURL = "https://claude.ai/oauth/authorize"
	ClaudeTokenURL     = "https://console.anthropic.com/v1/oauth/token"
	ClaudeClientID     = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"
	ClaudeRedirectURI  = "https://console.anthropic.com/oauth/code/callback"
	ClaudeScope        = "org:create_api_key user:profile user:inference"

	// GitHub Copilot (device flow).
	GitHubDeviceCodeURL  = "https://github.com/login/device/code"
	GitHubAccessTokenURL = "https://github.com/login/oauth/access_token"
	GitHubClientID       = "Iv1.b507a08c87ecfe98"
	GitHubScope          = "read:user"
	GitHubUserURL        = "https://api.github.com/user"
	CopilotTokenURL      = "https://api.github.com/copilot_internal/v2/token"

	// Gemini CLI / Google Code Assist (installed-app flow; the secret is public
	// in the open-source gemini-cli and is not confidential for installed apps).
	GoogleAuthURL      = "https://accounts.google.com/o/oauth2/v2/auth"
	GoogleTokenURL     = "https://oauth2.googleapis.com/token"
	GoogleUserInfoURL  = "https://www.googleapis.com/oauth2/v2/userinfo"
	GeminiClientID     = "681255809395-oo8ft2oprdrnp9e3aqf6av3hmdib135j.apps.googleusercontent.com"
	GeminiClientSecret = "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"
	GeminiScope        = "https://www.googleapis.com/auth/cloud-platform https://www.googleapis.com/auth/userinfo.email https://www.googleapis.com/auth/userinfo.profile"
	CodeAssistBaseURL  = "https://cloudcode-pa.googleapis.com"
	GeminiFreeTierID   = "free-tier"
	GeminiCallbackHost = "127.0.0.1"
)
