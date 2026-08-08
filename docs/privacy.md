# Privacy, Trust, and Retention

This is the canonical contract for what clai does with your text: where it can
go, how long it can stay, and what clai deliberately does not protect against.
Other clai documents carry short summaries and link here; where they differ,
this page is authoritative.

## The short version

- clai never runs a command for you. An accepted command is printed, copied,
  or inserted into your editable shell prompt — you review it and press Enter.
- Your OS user account is the trust boundary. Anything already running as you
  is trusted by clai.
- Pasted text is treated exactly like typed text. If you selected a remote
  provider, pasted intent may be sent to it.
- Clipboard copying is opt-in, and what clai copies stays on the clipboard
  until something else overwrites it.
- clai makes no secure-erasure, memory-wiping, or clipboard-history-deletion
  promises.

## Data flow at a glance

1. **Intent in.** You type an intent, or paste one with `Ctrl-V` or your
   terminal's own paste. clai keeps a bounded copy in TUI memory for the
   session.
2. **Compile.** When you submit the intent, it may be sent to the provider
   selected for that invocation. The intent is the request's primary input: it
   is sent whenever an allowed provider request proceeds. The context policy
   governs only the surrounding machine and repository context, not whether the
   intent is included in a request that is otherwise sent. The default local
   `rules` provider sends nothing off-host, and `local-only` against a
   non-loopback production endpoint fails closed: clai refuses the request
   instead of sending an intent-only one. See [providers.md](providers.md).
3. **Review.** Every suggestion passes independent validation, safety, and
   applicability gates. Nothing is delivered until you accept it.
4. **Delivery out.** Only the command you accepted is delivered — to stdout
   with `--print-command`, to the clipboard with `--copy`, or to the shell
   widget's private result file, which your shell reads and inserts at your
   cursor.

clai does not execute the command at any step.

## Trust boundary

**Trusted:**

- You, the interactive user.
- Your shell process and the clai process.
- Any other process running under your effective UID.
- The operating system's normal file-permission, sticky-directory, and
  desktop-clipboard enforcement.

**Not trusted, or not under clai's control:**

- Processes running as other OS users.
- Remote providers, beyond what a request actually sends them: your submitted
  intent, plus whatever surrounding context your policy allows.
- Clipboard managers and other desktop applications the OS permits to read
  the clipboard.
- Persistence caused by crashes, forced termination, OS swap, core dumps,
  terminal scrollback, shell history, or external observability tools.

clai's threat model stops at the OS user account. A process already running as
you can read your files, your terminal, and your clipboard regardless of what
clai does, so clai does not try to defend against it.

## Clipboard input (pasting into clai)

`Ctrl-V` explicitly asks the desktop clipboard for its current contents and
pastes them into the field you are editing.

Your terminal's own paste — bracketed paste, usually `Cmd-V` or
`Ctrl-Shift-V` — arrives as ordinary terminal input. clai bounds and sanitizes
it identically, but that path does not itself call the clipboard API; your
terminal already read the clipboard before clai saw anything.

**What clai retains.** clai keeps at most an 8,192-byte, complete-UTF-8,
sanitized prefix in the editable field's model state. The limit applies to the
whole field, not to each paste. Sanitizing turns tabs and line endings into
spaces and drops other control characters, so a multi-line snippet becomes one
line.

**Truncation and failure are visible.** If the paste does not fit, clai keeps
the bounded prefix and shows a notice naming the limit and the bytes retained.
Truncation at the byte limit is never silent. Sanitizing is separate and is not
announced: dropping control characters and invalid bytes, and folding tabs and
line endings into spaces, can shorten what you pasted without any notice. If
the clipboard read fails, the field is left unchanged and clai reports a
`Paste failed:` notice.

**What the limit does not bound.** The 8,192-byte ceiling governs what clai
keeps, not what the rest of the system handles. Before clai can apply it, the
operating system, the clipboard library, or the terminal's paste event may
already have materialized the entire source value. Pasting a very large
clipboard means clai retains only the bounded prefix — but the full value
existed in that pipeline first, and it is still on your clipboard afterwards.

**Where pasted text can go.** Pasted intent follows exactly the same data-flow
rules as typed intent; pasting creates no privacy exemption. If you selected a
remote provider, submitting the intent may transmit it to that provider. The
intent is the request's primary input, not an optional context field: the
context policy governs only the surrounding machine and repository context, and
it cannot hold the intent back from a request that is otherwise sent. What the
provider retains afterwards is governed by that provider's privacy policy, not
by your context policy.

**What pasting does not do.** Merely pasting text does not add it to a clai
history, write it to a log, or deliberately persist it to a local file. clai
does not promise to zeroize it in memory: the bounded value lives in ordinary
process memory for the session and may still be observable through swap, a
core dump, or a debugger.

## Clipboard output (clai copying for you)

Clipboard delivery is **opt-in**. clai writes to your clipboard only when you
select it:

- `--copy` on the command line;
- `"delivery": "clipboard"` in `config.json`;
- `CLAI_DELIVERY=clipboard` in the environment.

Only the final command you accepted is written. clai does not copy intents,
rejected candidates, or provider responses.

**How long it stays.** Once written, the OS desktop session owns it. The
command remains on the clipboard until something else overwrites it, and any
desktop application or clipboard manager the OS permits to read the clipboard
may read it or keep its own copy. Treat a copied command as visible to your
whole desktop session.

**clai never clears the clipboard automatically.** Automatic clearing would be
a misleading promise:

- a clipboard manager may already have taken the value into its history;
- a delayed clear can erase a newer value you or another application copied in
  the meantime;
- confirming the clipboard still holds exactly clai's value is
  backend-dependent, and confirming it still would not un-read prior readers;
- silent background behavior contradicts a tool whose safety model is built on
  visible, user-initiated actions.

If you copied something sensitive, copy something else over it.

**Failures are explicit.** If the clipboard backend is unavailable — a
headless session, a missing helper, no desktop clipboard — clai reports the
error and exits non-zero. It never silently switches to another delivery
channel, so an unavailable clipboard never becomes a surprise stdout print.

## Shell widget

The shell widget (`clai init fish|bash|zsh`) returns the accepted command to
your shell through a private temporary file. See
[shell/README.md](shell/README.md) for setup.

**Normal lifecycle:**

1. Your shell creates an empty regular file under `/tmp` with mode `0600`,
   using umask `077` plus an explicit `chmod`.
2. `clai widget` checks the path before starting the TUI: absolute, a regular
   file, no group or other permissions, and empty.
3. On acceptance, clai revalidates the command, re-checks that the path still
   names the same file, and writes one bounded single-line record.
4. Your shell reads the file, removes the pathname, and inserts the command at
   your cursor.

**The widget inserts; it never submits.** The command lands on your editable
prompt with the surrounding text preserved. clai does not send Enter and does
not execute anything. Cancelling, rejecting, or any error leaves the prompt
buffer untouched and exports no command.

**What the private file does and does not do.** Mode `0600` under a normally
configured sticky `/tmp` reduces accidental misuse and ordinary access by
*other* OS users. It is not an authenticated channel against processes running
as *you*.

Specifically, `clai widget` does not verify the file's owner UID and does not
check its link count. These are outside the defended boundary and are accepted
by the trust model rather than prevented:

- a same-UID process mutating the file;
- a same-UID process writing to the same inode through an already-open
  descriptor;
- replacing the pathname during the handoff window;
- racing your shell's read of the result.

clai's file-identity re-checks narrow the window and catch ordinary mistakes.
They are not a security guarantee against a same-UID adversary, and clai's
threat model does not posit one.

**Cleanup is best-effort.** Under normal operation the file is removed within
the same invocation. `clai widget` removes it on cancellation or error when
the pathname still names the file it originally inspected, and your shell
removes the pathname after the widget exits, whatever the exit status.

Under abnormal termination it can survive. A crash, a `SIGKILL`, a closed
terminal, a killed shell, or same-UID interference may leave a mode-`0600`
file holding one command in `/tmp`. clai promises neither guaranteed deletion
nor secure erasure; leftovers are subject only to your system's ordinary
`/tmp` cleanup.

## Retention summary

| Data | Normal lifetime | Abnormal or external retention | clai's guarantee |
|---|---|---|---|
| Clipboard input source | Read when you paste; bounded prefix kept for the TUI session | The full source may transiently exist in the OS, clipboard library, or terminal event, and remains on your clipboard | No clai history and no deliberate local-file persistence; no memory-zeroization promise |
| Submitted intent | Through the active compile request; sent whenever an allowed provider request proceeds | Remote-provider retention is governed by the selected provider, not by your context policy | Context policy scopes the surrounding context, not the intent; pasted and typed intent follow identical rules |
| Accepted clipboard output | Until another clipboard write replaces it | Clipboard managers and permitted desktop apps may retain it longer | Opt-in only; never auto-cleared by clai |
| Widget result file | Until the shell reads and removes it during the invocation | A crash, kill, shell termination, or same-UID interference may leave a mode-`0600` file in `/tmp` | Best-effort cleanup; no secure-erasure promise |
| Inserted shell command | Until you edit, clear, or run the prompt | Your shell may keep an executed command in its own history | clai inserts only; it never auto-executes |

## What clai does not promise

- **No same-UID isolation.** Processes running as you are inside the trust
  boundary by design.
- **No authenticated widget transport.** The result file is private, not
  authenticated; owner UID and link count are not verified.
- **No secure erasure.** File removal is ordinary unlinking, and cleanup is
  best-effort under abnormal termination.
- **No memory zeroization.** Bounded text lives in ordinary process memory and
  may reach swap or a core dump.
- **No clipboard-manager deletion.** clai cannot remove a copied command from
  clipboard history, and does not clear the clipboard at all.
