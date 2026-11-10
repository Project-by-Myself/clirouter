# Security policy

## Reporting a vulnerability

**Please do not open a public issue** for security problems.

Report privately through GitHub: **Security → Report a vulnerability**
(<https://github.com/Project-by-Myself/clirouter/security/advisories/new>).

Please include:

- version / commit, OS, and how you run it (bind address, whether `admin_password` / `api_keys` are set);
- steps to reproduce or a minimal PoC;
- impact (provider key disclosure, admin API takeover, DoS, …).

**Never send real API keys, OAuth tokens or a real `config.yaml`.** Use fake keys.

We aim to respond within 7 days, agree on a disclosure date with you (90 days at most by default),
and credit you in the release notes if you wish.

## Scope

In scope: the `clirouter` binary, the admin API `/admin/api/*`, the embedded dashboard, storage and
handling of keys/tokens, and the OAuth login flows.

Out of scope:

- an attacker who can already read/write `~/.clirouter` or run code as your user;
- anyone holding `admin_password` (admins are trusted: they can change a provider's `base_url`);
- the OAuth client IDs/secrets in `internal/auth/endpoints.go` — these are the **public** values of
  installed applications (gemini-cli, Copilot, Claude), not secrets;
- a provider rejecting or suspending an account because a subscription is used through a proxy.

## Supported versions

Only `main` / the latest release receives security fixes.
