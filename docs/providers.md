# Providers

clai compiles your intent through a pluggable provider. The default is the
local `rules` provider — deterministic, offline, no credentials. LLM-backed
providers are opt-in.

| Provider | Auth | Status |
|---|---|---|
| `rules` | none | default |
| `openrouter` | API key or browser sign-in (PKCE) | implemented |
| `anthropic` | API key only (no third-party OAuth) | implemented |
| `openai` | API key only (no third-party OAuth) | planned |

Regardless of provider, every suggestion still goes through the review →
validate → safety gates and is never auto-executed. When a provider has no
suggestion for an intent, clai says so instead of inventing a command, and
pressing `esc` while a request is loading cancels it without touching your
shell buffer.

## Selecting a provider

```sh
clai --provider openrouter            # flag
CLAI_PROVIDER=openrouter clai         # env
clai --provider openrouter --model openai/gpt-5
clai --provider openrouter --fallback-rules   # local rules if the LLM errors
clai --provider anthropic             # direct Anthropic (default claude-sonnet-5)
clai --provider anthropic --model claude-sonnet-5
```

`rules` remains the default; passing nothing behaves exactly as before.

## Context sent to remote providers

Runtime context sharing is controlled per invocation:

- `--context-policy local-only` sends no machine or repository context. It is
  the default for local rules and loopback endpoints, and fails closed if used
  with a remote endpoint.
- `--context-policy remote-minimal` is the default for non-loopback remote
  providers (OpenRouter and direct Anthropic). The request body contains the
  intent plus only normalized `os_family`, `shell_family`, and `project_kind`
  (`git`, `non-git`, or `unknown`).
- `--context-policy remote-explicit` requires the same invocation to include at
  least one repeatable `--share-context` flag. The only allowed fields are
  `working_directory`, `git_root`, and `git_branch`.

For example:

```sh
clai --provider openrouter --context-policy remote-explicit \
  --share-context working_directory --share-context git_branch
```

Sharing approval is not persisted. Duplicate fields are deduplicated
predictably. An approved field that is unavailable is omitted rather than
invented. `remote-minimal` never includes absolute paths, Git roots or branches,
Git remotes, host or user identity, shell history, command output, environment
dumps, repository contents, or credentials.

The API key is attached as an authorization header, not included in the JSON
request body. clai also creates an ephemeral, non-secret internal request receipt
covering the configured endpoint and its lexical classification, proxy mode,
policy, selected, redacted, and omitted fields, exact transmitted JSON bytes, and
their SHA-256 hash. Provider redirects are not followed, so the receipt endpoint
is always the only target clai allows for that request. Receipts are not persisted
or exposed through the current UI.

## Credentials

Resolution precedence: `--api-key` flag > environment variable > OS keyring >
the private `clai/credentials.json` file below Go's platform-specific
`os.UserConfigDir()`. Typical locations are `$XDG_CONFIG_HOME/clai/credentials.json`
(or `~/.config/clai/credentials.json`) on Linux and
`~/Library/Application Support/clai/credentials.json` on macOS. On Darwin and
Linux, the fallback fails closed: the `clai` credential directory, lock, and
credential file must be non-symlink, private objects, and pre-existing
group/other-accessible paths are rejected.
Parent paths are traversed without following symlinks, but clai does not claim
that every ancestor is private. On other platforms, the secure file fallback is
unsupported and fails closed rather than approximating Unix guarantees.
Environment variables per provider:

- `OPENROUTER_API_KEY`
- `ANTHROPIC_API_KEY`
- `OPENAI_API_KEY`

Auth uses `clai auth <login|status|logout> [options]`. Options must follow the
verb and use space-separated long forms. `--provider <name>` is valid for every
verb; `--api-key <value>` is valid only for `status`. Unknown, missing, duplicate,
or trailing arguments are rejected before credential, browser, or network work.

### `clai auth login`

```sh
clai auth login --provider openrouter
```

For OpenRouter this offers a browser sign-in using PKCE S256 and a bounded
loopback callback. OpenRouter documents a callback containing `code`, but does
not document a round-tripped `state` parameter or preservation of query values
inside `callback_url`. To avoid relying on an undocumented provider behavior,
clai accepts only an exact GET callback containing one non-empty bounded `code`
and no extra query parameters. If the browser cannot be launched, clai prints a
diagnostic and keeps waiting so you can open the already-printed URL manually.
The resulting API key is stored in your OS keyring, falling back to the private
platform-specific config file when the keyring is unavailable or an operation
fails. Pasting a key at the prompt skips the browser.

For `anthropic` and `openai` there is no third-party OAuth; paste an API key
from the provider console.

### `clai auth status` / `clai auth logout`

```sh
clai auth status --provider openrouter   # where the credential comes from
clai auth logout --provider openrouter   # remove stored credential
```

## Direct Anthropic

The `anthropic` provider calls the Anthropic Messages API directly through the
official `github.com/anthropics/anthropic-sdk-go` SDK — not an OpenAI-compatible
shim and not raw HTTP.

```sh
clai auth login --provider anthropic     # paste an API key from the console
clai auth status --provider anthropic     # where the credential comes from
clai --provider anthropic                 # compile with claude-sonnet-5
clai --provider anthropic --model claude-sonnet-5
```

- **Model IDs are bare Anthropic names.** Direct Anthropic uses `claude-sonnet-5`
  (the default) or another bare model ID via `--model`. This differs from
  OpenRouter, which namespaces models as `vendor/model` (for example
  `anthropic/claude-sonnet-5` or `openai/gpt-5`). Do not pass an OpenRouter-style
  slug to the direct provider.
- **Credentials are clai-owned and API-key only.** Resolution follows the same
  precedence as every provider — `--api-key` flag > `ANTHROPIC_API_KEY`
  environment variable > OS keyring > private config file. The SDK's ambient
  credential resolution is disabled: Anthropic CLI profiles, `ANTHROPIC_AUTH_TOKEN`,
  workload-identity federation, and environment base-URL overrides cannot
  substitute for or override the clai-resolved key. Only that key is sent, as the
  `x-api-key` header — never in the request body, receipt, hash input, or any
  diagnostic.
- **Context privacy is identical to other remote providers.** Direct Anthropic
  defaults to `remote-minimal`; `local-only` fails closed against the production
  endpoint, and `remote-explicit` includes only the invocation-approved
  `--share-context` fields.
- **Structured candidate output.** The request pins a strict `candidate/v1` JSON
  schema (a `command` and an `explanation`). The response is decoded strictly:
  both fields are required, and unknown fields, trailing JSON, or an empty
  command are rejected rather than shown.
- **Streaming is transport-only.** The SDK consumes the server-sent event stream
  incrementally, but the TUI stays on its loading state until one complete
  candidate is finalized. Partial text, partial JSON, thinking blocks, and
  command fragments never enter the review UI, the application result, or a
  receipt. There is no token-by-token display.
- **Cancellation and timeout.** Pressing `esc` during loading cancels the request
  with no late candidate; a cancelled request is not rendered as a provider
  failure. Each compile is bounded by a provider request timeout inside the
  application's outer timeout, and a deadline surfaces as a visible failure that
  exports nothing.
- **Refusals, truncation, and errors yield no candidate.** A model refusal, a
  `max_tokens` truncation, an interrupted or malformed stream, or an HTTP error
  (auth/permission, invalid request/model, rate limit, transient server/overload)
  is classified into a stable, secret-free diagnostic. None of these ever produce
  an accepted command, and raw upstream bodies, credentials, and hostile reason
  text are kept out of user diagnostics.
- **Fallback.** `--fallback-rules` retries through the local rules engine on an
  ordinary provider error only. Cancellation, deadlines, and an empty
  no-suggestion success do not fall back.
- **One attempt, one receipt.** SDK retries are disabled, so one compile is one
  network attempt and one ephemeral request receipt. Provider redirects are not
  followed, so the pinned endpoint is the only target clai contacts.

## Errors

Provider failures (network, auth, parse) render as `Error: ...` in the TUI
and never produce an accepted command. On a 401, re-run `clai auth login`.
With `--fallback-rules`, a provider error silently retries through the local
rules engine instead of showing the error. For direct Anthropic, refusals and
truncated (`max_tokens`) responses are treated as failures with no candidate,
not as partial output.
