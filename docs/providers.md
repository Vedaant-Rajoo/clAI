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

Regardless of provider, every suggestion still goes through independent review,
validation, safety, and applicability gates and is never auto-executed. When a provider has no
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
  intent plus normalized OS family, shell family, project kind, platform
  architecture, and presence/version status for this fixed tool allowlist only:
  `git`, `rg`, `fd`, `jq`, `curl`, `wget`, `tar`, `sed`, `awk`, `grep`, `lsof`,
  `ifconfig`, `ip`, and `bash`.
- `--context-policy remote-explicit` requires the same invocation to include at
  least one repeatable `--share-context` flag. It sends the same normalized
  remote-minimal baseline plus only selected-and-available explicit fields. The
  only explicit fields are `working_directory`, `git_root`, and `git_branch`.

For example:

```sh
clai --provider openrouter --context-policy remote-explicit \
  --share-context working_directory --share-context git_branch
```

Sharing approval is not persisted. Duplicate fields are deduplicated
predictably. An approved field that is unavailable is omitted rather than
invented. `local-only` omits the context object entirely. Tool facts are limited
to normalized `absent`, `present`, or `present:<numeric-version>` values;
executable paths, raw probe output, and parse errors are never transmitted.
`remote-minimal` also never includes absolute paths, Git roots or branches, Git
remotes, host or user identity, shell history, prior command output, environment
dumps, repository contents, or credentials.

## Testing against a local endpoint

`--dev-endpoint <url>` redirects a remote provider's requests to a loopback
server so you can exercise the complete request path — credential header,
context selection, request serialization, streaming, strict candidate decoding,
applicability, review, and shell export — without contacting the real provider
or spending money on API calls:

```sh
go run ./cmd/clai-stubprovider              # serves scripted scenarios on 127.0.0.1:8747
clai --provider anthropic --dev-endpoint http://127.0.0.1:8747
```

The URL must address a loopback host. Ordinary hostnames are rejected before any
network activity, including ones that happen to resolve to loopback, so this can
never redirect traffic off-host. It is not accepted for the local `rules`
provider, and it does not bypass credentials — a key is still resolved and sent
in the request header exactly as it would be in production. Because the endpoint
is loopback, the context policy defaults to `local-only` and the request carries
the intent alone; pass `--context-policy remote-minimal` (or `remote-explicit`
with `--share-context`) explicitly when you want to exercise context sharing. Every screen that can
show a candidate is labelled with the effective endpoint, so a stubbed session
cannot be mistaken for a real one. Run `go run ./cmd/clai-stubprovider --list` to
see the scenarios; the intent text selects one.

The API key is attached as an authorization header, not included in the JSON
request body. clai also creates an ephemeral, non-secret internal request receipt
covering the configured endpoint and its lexical classification, proxy mode,
policy, selected, redacted, and omitted fields, exact transmitted JSON bytes, and
their SHA-256 hash. Provider redirects are not followed, so the receipt endpoint
is always the only target clai allows for that request. Receipts are not persisted
or exposed through the current UI.

## Configuration and credential storage

On macOS and Linux, clai uses one preferred configuration base for both
behavioral settings and file-backed credentials:

- A non-empty, absolute `XDG_CONFIG_HOME` selects
  `$XDG_CONFIG_HOME/clai/config.json` and
  `$XDG_CONFIG_HOME/clai/credentials.json`.
- When `XDG_CONFIG_HOME` is unset or empty, clai uses
  `$HOME/.config/clai/config.json` and
  `$HOME/.config/clai/credentials.json`.
- Relative XDG_CONFIG_HOME values are invalid; clai neither resolves them against
  the working directory nor silently falls back to `$HOME/.config`.
- Windows keeps its existing platform user configuration directory and storage
  behavior. OS keychain service and user identities do not move on any platform.

The selected base may be a conventional shared configuration container. clai
creates any missing selected-base suffix and its own `clai` directory with
`0700` permissions, then stores `config.json`, `credentials.json`, locks, and
temporary files as private objects; both data files use `0600` permissions.

## One-way macOS migration

On Darwin, when state exists only under
`$HOME/Library/Application Support/clai`, clai migrates it one way to the
preferred root. A preferred file is authoritative whenever it exists: legacy
state is retired without merging, and a corrupt preferred config.json keeps the
existing warning-and-default behavior instead of falling back to stale legacy
settings. Config migration preserves only the five `config-file/v1` fields and
never copies credential-shaped fields into `config.json`.

Credential Resolve, Store, Delete, and successful keyring cleanup reconcile both file locations,
preserve unrelated preferred providers, and retire stale legacy copies so a
removed secret cannot reappear. Existing selected bases may keep conventional
permissions such as `0755`; newly created suffixes and `clai` remain `0700`, and
data files remain `0600`. The selected base root itself may be a symlink that is
canonicalized once, but `clai` and all descendants are opened without following symlinks,
with descriptor-relative identity checks that fail closed on substitution.

Mixed-version downgrade and concurrent use with a pre-migration binary are
unsupported after migration. Upgrade every clai binary that shares these roots
before continuing to modify settings or credentials.

## Credentials

Resolution precedence remains `--api-key` flag > environment variable > OS
keyring > the preferred private `credentials.json` file described above. The
root migration does not rename or move keychain entries. On Darwin and Linux,
the file fallback fails closed when the `clai` directory, lock, temporary file,
or credential file is a symlink, non-private, non-regular, or substituted while
open. Parent paths are traversed without following symlinks, but clai does not
claim every existing ancestor is private. On other platforms, keychain and
secure file-fallback behavior remain unchanged; unsupported secure fallback
fails closed rather than approximating Unix guarantees.
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
  endpoint, and `remote-explicit` sends the remote-minimal baseline plus only
  invocation-approved, selected-and-available `--share-context` fields.
- **Structured candidate output.** The request pins strict `candidate/v2` JSON:
  required `command` and `explanation` fields plus an optional bounded
  `requirements` array for tool, shell, or OS applicability. A v1-shaped response
  remains valid with no requirements. Unknown fields, trailing JSON, malformed
  requirements, or an empty command are rejected rather than shown.
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
