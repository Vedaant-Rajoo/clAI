# Widget and Clipboard Trust & Retention Design

**Status:** Approved design
**Date:** 2026-08-07
**Scope:** Shell-widget result handoff, clipboard input, clipboard output, and their user-facing documentation

## Context

clai has two convenience paths that cross process or desktop-session boundaries:

1. Shell widgets ask `clai widget` to write an accepted command to a private temporary file, then insert that command into the user's editable shell prompt.
2. The TUI can read clipboard text through `Ctrl-V`, and normal CLI delivery can copy an accepted command to the system clipboard.

The implementation already validates commands, bounds retained editable input, uses private result files, and never submits the shell prompt automatically. What was missing was a single explicit contract describing who is trusted, how long data can remain, what clai does not protect against, and how pasted text follows provider privacy rules.

## Goals

- State a clear trust boundary that matches the current implementation.
- Preserve the product invariant that generated commands are inserted for review and never silently executed.
- Explain clipboard input and output behavior without implying secure erasure or private clipboard storage.
- Explain normal and abnormal result-file retention.
- Give newcomers a short plain-language summary while keeping one canonical detailed contract.
- Pin user-visible CLI help where practical and verify the documented behavior against the implementation.

## Non-goals

- Redesigning the widget transport around inherited descriptors, pipes, sockets, locks, or authenticated content.
- Treating another process running under the same UID as an adversary.
- Adding timed clipboard clearing or attempting to erase clipboard-manager history.
- Providing memory zeroization or forensic secure deletion.
- Changing provider privacy selection, command validation, safety policy, or delivery defaults.

## Product decisions

1. **The OS user account is the widget trust boundary.** Processes running under the same UID are trusted.
2. **Clipboard output uses OS-owned persistence.** It remains opt-in, and clai does not automatically clear it.
3. **`docs/privacy.md` is the canonical contract.** README, shell docs, provider docs, and CLI help carry short contextual summaries and link back where links are available.

## Trust model

### Trusted

- The interactive user invoking clai.
- The user's shell process and clai process.
- Other processes running under the same effective UID, for purposes of the widget handoff.
- The operating system's normal sticky-directory, file-permission, and desktop-clipboard enforcement.

### Not trusted or not controlled by clai

- Processes running as other OS users.
- Remote providers beyond the data-sharing policy selected for that invocation.
- Clipboard managers and other desktop applications that the OS permits to read or retain clipboard contents.
- Persistence caused by crashes, forced termination, OS swap, core dumps, terminal buffers, shell history, or external observability tools.

### Explicit boundary statement

The widget's private temporary file protects against accidental misuse and ordinary cross-user access on a normally configured sticky `/tmp`. It is **not** an authenticated channel against another process running under the same UID. `clai widget` does not verify owner UID or link count, and a same-UID process may be able to mutate the open inode or race/replace the pathname during the handoff window. This is accepted by the product trust model and must be documented rather than hidden behind stronger security language.

## Required invariants

### Shell widget

- The shell adapter creates an absolute, initially empty, regular result file beneath `/tmp` with mode `0600`.
- `clai widget` validates the result file before starting the TUI and revalidates its type, permissions, and identity before writing.
- Only a command that passes the final validity, applicability, and safety export gates is written.
- The accepted command is one bounded record: at most 8,192 bytes and without CR, LF, NUL, or prohibited terminal-control characters.
- On normal success, the shell reads the result, removes the pathname, and inserts the command into the editable prompt.
- The widget never submits the prompt, sends Enter, or executes the command.
- Cancellation, refusal, validation failure, or widget errors preserve the existing prompt and export no command.

### Clipboard input

- `Ctrl-V` explicitly requests a read of the current desktop clipboard.
- Terminal bracketed paste is handled as paste input but does not itself call the desktop clipboard API.
- The clipboard library, OS, or terminal event may transiently materialize the full source value before clai applies its bound.
- clai retains at most an 8,192-byte, complete-UTF-8, sanitized prefix in editable model state.
- Truncation or refusal is visible to the user; shortening must never be silent.
- A clipboard-read failure leaves the editable field unchanged and reports an actionable paste failure.
- Merely pasting text does not cause clai to add it to a history, log it, or deliberately persist it to a local file.
- clai does not promise in-memory zeroization.

### Clipboard output

- Clipboard delivery occurs only when selected explicitly through `--copy`, delivery configuration, or the delivery environment setting.
- Only the final command accepted by the user is written to the clipboard.
- clai does not silently fall back to clipboard delivery from another failed delivery mode.
- If the clipboard backend is unavailable or headless, delivery fails explicitly.
- After a successful write, the OS desktop session owns retention. The command remains until overwritten and may be read or retained by permitted desktop applications or clipboard managers.
- clai never automatically clears the clipboard.

## Data-flow contract

Clipboard-derived text follows the same data-flow rules as text entered through the keyboard:

1. While editing an **intent**, the bounded text remains in TUI model state for the session.
2. When the intent is submitted, it may be sent to the selected provider according to the invocation's privacy mode and provider documentation. Pasting does not create a separate privacy exemption.
3. While editing a **command**, the bounded text remains subject to normal validation, safety, and applicability review.
4. After acceptance, the command may be delivered through stdout, the shell-widget result file, or opt-in clipboard output.

Documentation must not say or imply that pasted content always stays local when a remote provider is selected.

## Retention contract

| Data | Normal lifetime | Abnormal or external retention | clai guarantee |
| --- | --- | --- | --- |
| Clipboard input source | Read when the user invokes paste; bounded prefix retained for the TUI session | Full source may transiently exist in clipboard APIs or terminal events; OS/apps may already retain it | No clai history or deliberate local-file persistence; no memory-zeroization promise |
| Submitted intent | Through the active compile request | Remote provider retention is governed by the selected provider and privacy mode | Same policy for pasted and typed intent |
| Accepted clipboard output | Until another clipboard write replaces it | Clipboard managers or desktop apps may retain it longer | Opt-in only; never auto-cleared |
| Widget result file | Until the shell reads and removes it during the invocation | Crash, kill, shell termination, or same-UID interference may leave a private file in `/tmp` | Best-effort cleanup; no secure-erasure promise |
| Inserted shell command | Until the user edits, clears, or executes the prompt | The shell may retain an executed command in its own history | clai inserts only; it never auto-executes |

## Why clai does not auto-clear clipboard output

Automatic clearing would provide a misleading promise:

- A clipboard manager may already have copied the value into history.
- A delayed clear can erase a newer value copied by the user or another application.
- Reliably checking that the clipboard still contains the exact clai value is backend-dependent and still does not erase prior readers or history.
- Background clearing adds hidden behavior to a tool whose safety model emphasizes visible, user-controlled actions.

The truthful contract is therefore persistence until overwritten, with an explicit warning at the point where clipboard delivery is described.

## Widget cleanup behavior

Normal ownership is split deliberately:

1. The shell creates the private file.
2. `clai widget` removes it on cancellation or error when the pathname still identifies the originally inspected object.
3. On success, clai leaves it for the shell.
4. The shell reads it, removes the pathname, then changes the editable prompt.

The documentation must call this **best-effort cleanup**, not guaranteed deletion. It must also avoid implying that mode `0600` defends against processes running as the same user.

## Documentation architecture

### Canonical: `docs/privacy.md`

Create a newcomer-readable document containing:

- A short data-flow overview.
- The same-UID and desktop-session trust boundaries.
- Clipboard input behavior, transient source materialization, the retained 8 KiB limit, and remote-provider flow.
- Clipboard output selection, persistence, visibility to other desktop software, and the no-auto-clear rationale.
- Widget lifecycle, race window, cleanup, crash residue, and the insert-never-execute invariant.
- Explicit non-promises: no same-UID isolation, secure erasure, memory zeroization, or clipboard-manager deletion.

Normative wording should live here. Other documents should summarize and link rather than copy whole sections.

### `README.md`

Add a concise **Trust and retention** section that tells newcomers:

- Generated commands are inserted for review and never automatically executed.
- Clipboard delivery is opt-in and persists until overwritten.
- Pasted intent may be sent to the selected remote provider.
- The detailed contract is in `docs/privacy.md`.

### `docs/shell/README.md`

Extend the existing widget protocol section to state:

- The result file is private and short-lived under normal operation.
- Processes under the same UID are inside the trust boundary.
- Same-UID mutation or replacement during handoff is not prevented.
- Cleanup is best-effort and crashes may leave a private `/tmp` file.
- The shell inserts but does not execute.
- The canonical privacy/trust contract is `../privacy.md`.

### `docs/providers.md`

State explicitly that pasted intent is treated exactly like typed intent and may be transmitted when a remote provider is selected. Link to `privacy.md`.

### CLI help

Extend the `--copy` description with concise wording equivalent to:

> Copy the accepted command to the system clipboard. It remains there until overwritten, may be retained by desktop software, and is not automatically cleared by clai.

Do not overload hidden widget help with the entire threat model; keep its protocol summary and point users to documentation where practical.

### Traycer artifacts

Update the execution/review record to mark the widget/clipboard product-policy decision resolved and record:

- same-UID trust was explicitly accepted;
- clipboard output is opt-in with OS-owned persistence and no auto-clear;
- the canonical contract location is `docs/privacy.md`.

## Error handling and user messaging

- Clipboard input failure: field unchanged; terminal-safe `Paste failed: ...` notice.
- Clipboard output failure: explicit delivery error; no silent channel change.
- Widget cancellation/refusal: preserve prompt; no exported command.
- Widget validation or I/O failure: preserve prompt and report the failure through the existing exit/error protocol.
- Documentation must distinguish **trusted boundary** from **guaranteed cleanup** and **retained model bound** from **source-I/O bound**.

## Verification

Implementation of this design must:

1. Add or update tests that pin the new `--copy` help warning.
2. Check that README, shell docs, provider docs, and CLI help agree with `docs/privacy.md`.
3. Confirm links resolve and avoid duplicated normative passages likely to drift.
4. Verify no runtime behavior was unintentionally changed.
5. Run `gofmt` on changed Go files, `go vet ./...`, and `go test ./...`.

Existing widget, delivery, and bounded-input tests remain the behavioral evidence. Same-UID race tests are not required because the contract explicitly accepts that boundary rather than claiming to prevent it.

## Acceptance criteria

- A newcomer can determine whether clai will execute a command, where pasted intent may go, and how long copied output may remain without reading source code.
- The canonical document states the same-UID and desktop-session trust boundaries without euphemism.
- Clipboard input and output are documented separately.
- The retained 8 KiB bound is not misrepresented as preventing transient full-source reads by the OS, terminal, or clipboard library.
- Clipboard output is described as opt-in, persistent until overwritten, potentially retained by desktop software, and never auto-cleared by clai.
- Widget cleanup is described as immediate under normal operation and best-effort under abnormal termination.
- No document claims secure erasure, memory zeroization, same-UID isolation, or authenticated widget transport.
- CLI help and the full Go test suite pass after documentation integration.
