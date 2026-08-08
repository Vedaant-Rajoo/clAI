# Shell Integration

`clai init` prints the integration script embedded in the installed `clai`
binary. It supports Fish, Bash, and Zsh:

```text
clai init fish
clai init bash
clai init zsh
```

Source or evaluate the command for your shell during interactive startup. The
default keybinding is `ctrl-x ctrl-a`.

| Shell | Current session | Persistent setup |
|---|---|---|
| Fish | `clai init fish \| source` | Add the same line to `~/.config/fish/config.fish` |
| Bash | `eval "$(clai init bash)"` | Add the same line to `~/.bashrc` |
| Zsh | `eval "$(clai init zsh)"` | Add the same line to `~/.zshrc` |

The `clai` binary must be on `PATH` when the startup file is loaded. See the
shell-specific pages for configuration examples:

- [Fish](fish.md)
- [Bash](bash.md)
- [Zsh](zsh.md)

## Interaction Model

Press the binding, enter an intent, review the suggestion, and accept it. The
integration inserts the accepted command at the cursor, preserving any text
already on the prompt, and does not execute the command. You remain
responsible for reviewing, editing, and pressing Enter.

Widget mode requires a Unix-like system with a controlling terminal; it is
not supported on Windows.

Cancelling with `esc` or `ctrl-c`, rejecting the suggestion, or leaving the TUI
without accepting keeps the existing command-line buffer unchanged.

Each shell-specific binding can be configured before loading the integration.
If the requested key sequence is already in use, `clai` reports the collision
instead of silently replacing the existing binding.

## Scripting With `--print-command`

The shell widget uses the result-file adapter described below, but the existing
stdout interface remains available for scripts:

```sh
clai --print-command
```

After acceptance, this prints only the accepted command to stdout. Cancellation
prints no command, which keeps existing command-substitution workflows
compatible. For example, in a POSIX-style shell:

```sh
command_to_review=$(clai --print-command)
printf '%s\n' "$command_to_review"
```

This mode also does not execute the command.

## Internal Adapter Contract

`clai widget` is an internal integration detail used by the generated shell
scripts, not the normal user-facing entry point:

```text
clai widget --shell <fish|bash|zsh> --result-file <absolute-path>
```

The caller creates an empty regular result file at an absolute path with no
group or other permissions (the shipped adapters use mode `0600`). On
acceptance, `clai` validates the command, writes it to that file, and exits
successfully. On cancellation it exits without writing a command; other errors
also leave the prompt buffer unchanged.

The shipped adapters create the file under the system sticky directory `/tmp`
with a restrictive umask, enforce mode `0600`, and remove it after success,
cancellation, or failure. They intentionally do not trust an inherited
`TMPDIR`, because a writable non-sticky directory could allow result-path
replacement. They read the result only after a successful widget exit and then
insert the accepted command at the current cursor without executing it. Existing
prompt text on both sides of the cursor remains editable. Bash 3.2 reaches the
same cursor splice through a Readline kill-ring macro because its Readline does
not expose a writable cursor to shell functions.

### Trust and retention

The result file is private and short-lived: under normal operation it is
created, written once, read, and removed within the same invocation.

Your OS user account is the trust boundary. Mode `0600` under a normally
configured sticky `/tmp` reduces ordinary access by *other* OS users, but any
process already running under your UID is trusted by design. `clai widget`
does not verify the file's owner UID or its link count, so it does not prevent
a same-UID process from mutating the file or replacing the pathname during the
handoff window. clai's checks — that the path is absolute, regular, private,
and empty before the TUI starts, and still the same file before the write —
narrow that window and catch ordinary mistakes. They are not a security
guarantee against a same-UID adversary, and clai's threat model does not posit
one.

Cleanup is best-effort. A crash, a `SIGKILL`, a closed terminal, or a killed
shell can leave a mode-`0600` file holding one command in `/tmp`, subject only
to your system's ordinary `/tmp` cleanup. clai promises neither guaranteed
deletion nor secure erasure.

The shell inserts the accepted command into your editable prompt and never
submits it: clai does not send Enter and does not execute anything.

The complete trust and retention contract is in [privacy.md](../privacy.md).
