# Providers

clai compiles your intent through a pluggable provider. The default is the
local `rules` provider — deterministic, offline, no credentials. LLM-backed
providers are opt-in.

| Provider | Auth | Status |
|---|---|---|
| `rules` | none | default |
| `openrouter` | API key or browser sign-in (PKCE) | implemented |
| `anthropic` | API key only (no third-party OAuth) | planned |
| `openai` | API key only (no third-party OAuth) | planned |

Regardless of provider, every suggestion still goes through the review →
validate → safety gates and is never auto-executed.

## Selecting a provider

```sh
clai --provider openrouter            # flag
CLAI_PROVIDER=openrouter clai         # env
clai --provider openrouter --model openai/gpt-5
clai --provider openrouter --fallback-rules   # local rules if the LLM errors
```

`rules` remains the default; passing nothing behaves exactly as before.

## Credentials

Resolution precedence: `--api-key` flag > environment variable > OS keyring >
`~/.config/clai/credentials.json` (0600). Environment variables per provider:

- `OPENROUTER_API_KEY`
- `ANTHROPIC_API_KEY`
- `OPENAI_API_KEY`

### `clai auth login`

```sh
clai auth login --provider openrouter
```

For OpenRouter this offers a browser sign-in (PKCE): it opens
`openrouter.ai/auth`, you approve, and the resulting API key is stored in
your OS keyring (falling back to the config file on headless systems).
Pasting a key at the prompt skips the browser.

For `anthropic` and `openai` there is no third-party OAuth; paste an API key
from the provider console.

### `clai auth status` / `clai auth logout`

```sh
clai auth status --provider openrouter   # where the credential comes from
clai auth logout --provider openrouter   # remove stored credential
```

## Errors

Provider failures (network, auth, parse) render as `Error: ...` in the TUI
and never produce an accepted command. On a 401, re-run `clai auth login`.
With `--fallback-rules`, a provider error silently retries through the local
rules engine instead of showing the error.
