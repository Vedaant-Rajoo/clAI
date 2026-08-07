package main

import (
	"fmt"
	"io"
)

// commandHelp is the single source of truth for per-command help pages.
type commandHelp struct {
	name    string
	summary string // one-liner for the top-level command list
	long    string // full page: Usage / description / Flags / Examples
	hidden  bool   // excluded from the top-level list (internal commands)
}

var commands = []commandHelp{
	{
		name:    "auth",
		summary: "Manage provider credentials (login, status, logout)",
		long: `Manage provider credentials.

Usage:
  clai auth login [--provider <name>]
  clai auth status [--provider <name>] [--api-key <value>]
  clai auth logout [--provider <name>]

Use -h or --help anywhere, or help as the trailing argument, to show this page.

Options must follow the subcommand and use the documented space-separated form.

Subcommands:
  login      Sign in (OpenRouter browser flow, or paste an API key)
  status     Show whether credentials are configured and their source
  logout     Remove stored credentials

Flags:
  --provider name    provider: openrouter (default) | anthropic | openai
  --api-key key      API key override (status only)

Examples:
  clai auth login --provider openrouter
  clai auth status --provider openrouter
  clai auth status --provider openrouter --api-key <value>
  clai auth logout --provider openrouter

Run "clai help storage" for credential locations and migration details.
`,
	},
	{
		name:    "init",
		summary: "Print shell integration script for fish, bash, or zsh",
		long: `Print the shell integration script for your shell.

Usage:
  clai init <fish|bash|zsh>

The script binds a widget that opens clai and inserts the accepted
command into your prompt line.

Examples:
  clai init fish | source
  eval "$(clai init bash)"
  eval "$(clai init zsh)"
`,
	},
	{
		name:    "version",
		summary: "Print the clai version",
		long: `Print the clai version.

Usage:
  clai version
`,
	},
	{
		name:    "storage",
		summary: "Explain configuration and credential storage",
		hidden:  true,
		long: `Configuration and credential storage.

On macOS and Linux, a non-empty $XDG_CONFIG_HOME selects:
  $XDG_CONFIG_HOME/clai/config.json
  $XDG_CONFIG_HOME/clai/credentials.json

XDG_CONFIG_HOME must be absolute. A relative XDG_CONFIG_HOME is invalid.
When it is unset or empty, clai uses:
  $HOME/.config/clai/config.json
  $HOME/.config/clai/credentials.json

Windows keeps its platform user configuration directory. OS keychain entries
do not move. On Darwin, old-only macOS Application Support state migrates one way;
preferred files win. Mixed-version downgrade after migration is unsupported.
`,
	},
	{
		name:    "widget",
		summary: "Internal shell-adapter entry point",
		hidden:  true,
		long: `Internal shell-adapter entry point.

This command is invoked by the shell scripts printed by "clai init";
it is not meant to be run directly.

Usage:
  clai widget --shell <fish|bash|zsh> --result-file <path>

Flags:
  --shell name        active shell: fish | bash | zsh
  --result-file path  caller-created file the accepted command is written to
  --provider name     provider: rules | openrouter | anthropic
                      (default: rules, or $CLAI_PROVIDER, or config.json)
  --model name        model override for LLM providers
                      (default: $CLAI_MODEL, or config.json, or the provider's own)
  --api-key key       API key override for LLM providers
  --fallback-rules    fall back to local rules when the provider errors
  --context-policy p  context policy: local-only | remote-minimal | remote-explicit
  --share-context f   share a field with remote-explicit (repeatable):
                      working_directory | git_root | git_branch
  --dev-endpoint url  development only: send provider requests to a loopback
                      endpoint instead of the real provider

Exit codes:
  0  command accepted and written to the result file
  1  error
  2  usage error
  3  cancelled by the user
`,
	},
}

func lookupCommand(name string) (commandHelp, bool) {
	for _, c := range commands {
		if c.name == name {
			return c, true
		}
	}
	return commandHelp{}, false
}

func printMainHelp(w io.Writer) {
	fmt.Fprint(w, `clai - turn a natural-language request into a shell command

Usage:
  clai [flags]                start the interactive TUI
  clai <command> [arguments]

Commands:
`)
	for _, c := range commands {
		if c.hidden {
			continue
		}
		fmt.Fprintf(w, "  %-10s %s\n", c.name, c.summary)
	}
	fmt.Fprint(w, `
Flags:
  --copy             copy the accepted command to the clipboard
                     (or delivery setting in config.json)
  --print-command    print the accepted command to stdout
                     (or delivery setting in config.json)
  --provider name    provider: rules | openrouter | anthropic
                     (default: rules, or $CLAI_PROVIDER, or config.json)
  --model name       model override for LLM providers
                     (default: $CLAI_MODEL, or config.json, or the provider's own)
  --api-key key      API key override for LLM providers
  --fallback-rules   fall back to local rules when the provider errors
  --context-policy p context policy: local-only | remote-minimal | remote-explicit
  --share-context f  share a field with remote-explicit (repeatable):
                     working_directory | git_root | git_branch
  --dev-endpoint url development only: send provider requests to a loopback
                     endpoint instead of the real provider
  --version          print version and exit

Environment:
  CLAI_PROVIDER      default provider: rules | openrouter | anthropic
  CLAI_MODEL         default model for LLM providers
  CLAI_DELIVERY      default delivery: clipboard | stdout | insert

Flags override environment variables; both override config.json.

Run "clai help storage" for configuration paths and migration details.

Use "clai help <command>" or "clai <command> help" for more information.
`)
}

func printCommandHelp(w io.Writer, c commandHelp) {
	fmt.Fprint(w, c.long)
}

// runHelp handles `clai help [command]`.
func (c cli) runHelp(args []string) int {
	if len(args) == 0 {
		printMainHelp(c.stdout)
		return exitOK
	}
	cmd, ok := lookupCommand(args[0])
	if !ok {
		fmt.Fprintf(c.stderr, "clai help: unknown command %q\nRun 'clai help' for usage.\n", args[0])
		return exitUsage
	}
	printCommandHelp(c.stdout, cmd)
	return exitOK
}

// isHelpArg reports whether arg requests help in any accepted spelling.
func isHelpArg(arg string) bool {
	return arg == "help" || arg == "-h" || arg == "--help"
}
