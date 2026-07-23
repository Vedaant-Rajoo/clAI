# Bash Integration

The Bash integration opens `clai` from a Readline keybinding and inserts an
accepted command into the current Bash command-line buffer without executing it.
Bash 4+ inserts at the cursor; the Bash 3.2 compatibility path currently appends
at the end of the buffer.

See the [shell integration overview](README.md) for shared behavior and the
internal adapter contract.

## Setup

Make sure `clai` is on `PATH`, then load the integration in the current Bash
session:

```bash
eval "$(clai init bash)"
```

For permanent use, add that line to `~/.bashrc`:

```bash
eval "$(clai init bash)"
```

Restart Bash or reload the configuration:

```bash
source ~/.bashrc
```

The default binding is `ctrl-x ctrl-a`. Press it, enter an intent, review the
suggested command, and accept it. On Bash 4+, the accepted command is inserted
at the cursor without running it, so surrounding prompt text is preserved. On
Bash 3.2, it is appended at the end of the existing buffer. Cancelling with `esc`
or `ctrl-c` leaves the existing buffer unchanged.

## Configuration

Set configuration variables before evaluating `clai init bash`.

Use a custom Readline key sequence:

```bash
export CLAI_BASH_BINDING='\C-g'
eval "$(clai init bash)"
```

Use a `clai` binary that is not on `PATH`:

```bash
export CLAI_COMMAND="$HOME/.local/bin/clai"
eval "$(clai init bash)"
```

Add persistent variable settings before the initialization line in `~/.bashrc`.
If the selected key sequence is already bound, the integration reports the
conflict rather than replacing the existing binding.

Bash 4 and newer use `bind -x` with `READLINE_LINE` and `READLINE_POINT`. Bash
3.2, including the Bash shipped by macOS, uses a Readline macro compatibility
path that preserves the original buffer on cancellation and errors. That path
requires Emacs editing mode; run `set -o emacs` before loading the integration.
Bash 3.2 cannot enumerate unrelated existing `bind -x` mappings, so
binding-collision detection is best-effort on that version.

## Troubleshooting

Check that Bash can find the binary:

```bash
command -v clai
```

Inspect the generated integration without evaluating it:

```bash
clai init bash
```

Test the scripting-compatible stdout mode separately:

```bash
command_to_review=$(clai --print-command)
printf '%s\n' "$command_to_review"
```

An accepted command is printed but not executed. Cancellation produces no
command. The interactive widget itself uses a protected temporary result file,
removes it on every completion path, and changes the prompt only after a
successful acceptance.
