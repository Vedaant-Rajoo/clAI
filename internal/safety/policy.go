package safety

import (
	"path"
	"slices"
	"strings"

	"codeberg.org/newedia/clai/internal/shellsyntax"
	"codeberg.org/newedia/clai/internal/textsafe"
	"codeberg.org/newedia/clai/internal/validate"
)

type Decision string

const (
	Allow Decision = "allow"
	Warn  Decision = "warn"
	Block Decision = "block"
)

type Result struct {
	Decision Decision
	Reasons  []string
}

type finding struct {
	decision Decision
	reason   string
}

// Evaluate classifies command without executing a shell or evaluating any
// expansion. Unsupported parser issues are conservative: known evaluation
// constructs block, while every other uncertainty warns and never allows.
func Evaluate(command string) Result {
	var findings []finding
	add := func(decision Decision, reason string) {
		for _, existing := range findings {
			if existing.decision == decision && existing.reason == reason {
				return
			}
		}
		findings = append(findings, finding{decision: decision, reason: reason})
	}

	if strings.TrimSpace(command) == "" {
		add(Block, "Empty command.")
	}
	if textsafe.ContainsProhibitedCommandFormat(command) {
		add(Block, "Command contains a prohibited invisible or bidirectional format character.")
	}
	if len(command) > validate.MaxCommandBytes {
		add(Block, "Command exceeds the maximum size eligible for safety analysis.")
		return aggregate(findings)
	}

	parsed := shellsyntax.Parse(command)
	for _, pipeline := range parsed.List.Pipelines {
		for commandIndex, simple := range pipeline.Commands {
			resolved := shellsyntax.ResolveExecutable(simple)
			if !resolved.Found {
				resolved = resolveEnvSplitDispatch(simple)
			}
			if !resolved.Found {
				resolved = resolveUnsupportedDispatch(simple)
			}
			if resolved.Found {
				classifyExecutable(resolved, commandIndex, len(pipeline.Commands), add)
			} else if len(simple.Words) > 0 || len(simple.Redirects) > 0 {
				add(Warn, "An executable position could not be resolved safely.")
			}
			classifyRedirects(simple.Redirects, add)
		}
	}
	classifyIssues(parsed.Issues, add)

	if len(findings) == 0 {
		add(Warn, "Command is not recognized by the local safety policy.")
	}
	return aggregate(findings)
}

func classifyExecutable(resolved shellsyntax.ExecutableResolution, commandIndex, commandCount int, add func(Decision, string)) {
	base := resolved.Base
	args := wordValues(resolved.Arguments)
	gitArgs, gitCommandKnown := gitCommand(args)

	switch {
	case slices.Contains([]string{"rm", "dd", "truncate", "mkfs", "mkfs.ext2", "mkfs.ext3", "mkfs.ext4", "mkfs.xfs", "mkfs.btrfs", "fdisk", "parted", "wipefs", "shutdown", "reboot", "halt", "poweroff", "kill", "pkill", "killall"}, base):
		add(Block, "Command invokes a blocked destructive executable: "+base+".")
	case slices.Contains([]string{"sudo", "doas", "su"}, base):
		add(Block, "Command invokes an elevated-privilege executable: "+base+".")
	case slices.Contains([]string{"eval", "source", "."}, base):
		add(Block, "Command invokes shell evaluation: "+base+".")
	case isShell(base) && commandIndex > 0:
		add(Block, "A shell executable receives pipeline input for evaluation.")
	case resolved.ShellEvaluation:
		add(Block, "Command invokes shell command-string evaluation.")
	case base == "git" && ((gitCommandKnown && gitDestructive(gitArgs)) || (!gitCommandKnown && gitDestructiveUnderUncertainty(args))):
		add(Block, "Git command contains a blocked destructive operation.")
	case (base == "curl" || base == "wget") && commandIndex < commandCount-1:
		add(Block, "Piping downloader output is blocked by local policy.")
	case slices.Contains([]string{"mv", "cp", "chmod", "chown", "mkdir", "touch", "curl", "wget"}, base):
		add(Warn, "Command may modify files, permissions, or download remote content.")
	case base == "git" && gitCommandKnown && startsWithAnyArgument(gitArgs, "add", "commit", "checkout", "switch", "merge", "rebase"):
		add(Warn, "Git command may modify repository state.")
	case base == "docker" && startsWithAnyArgument(args, "run", "build", "pull"):
		add(Warn, "Docker command may create containers, build images, or download remote content.")
	case slices.Contains([]string{"pwd", "ls", "find", "rg", "du", "df", "date", "hostname", "go", "ps", "lsof", "printenv", "printf", "ifconfig", "echo"}, base):
		add(Allow, "Command appears to be read-only.")
	case base == "git" && gitCommandKnown && startsWithAnyArgument(gitArgs, "status", "diff", "log", "branch", "remote", "tag"):
		add(Allow, "Command appears to be read-only.")
	case base == "docker" && startsWithAnyArgument(args, "ps", "images"):
		add(Allow, "Command appears to be read-only.")
	default:
		add(Warn, "Command is not recognized by the local safety policy: "+base+".")
	}

}

// resolveEnvSplitDispatch handles env -S/--split-string without invoking env.
// The split argument is parsed by the same bounded, non-evaluating parser and
// its words are substituted into a synthetic env command for policy resolution.
func resolveEnvSplitDispatch(command shellsyntax.SimpleCommand) shellsyntax.ExecutableResolution {
	words := command.Words
	index := len(command.Assignments)
	if index >= len(words) || safetyExecutableBase(words[index].Value) != "env" {
		return shellsyntax.ExecutableResolution{}
	}

	for optionIndex := index + 1; optionIndex < len(words); optionIndex++ {
		value := words[optionIndex].Value
		var splitValue, precedingShortOptions string
		remainderStart := optionIndex + 1
		switch {
		case value == "--split-string":
			if remainderStart >= len(words) {
				return shellsyntax.ExecutableResolution{}
			}
			splitValue = words[remainderStart].Value
			remainderStart++
		case strings.HasPrefix(value, "--split-string="):
			splitValue = strings.TrimPrefix(value, "--split-string=")
		default:
			var found bool
			splitValue, precedingShortOptions, remainderStart, found = envShortSplitOperand(words, optionIndex)
			if !found {
				continue
			}
		}

		split := shellsyntax.Parse(splitValue)
		if len(split.List.Pipelines) != 1 || len(split.List.Pipelines[0].Commands) != 1 {
			return shellsyntax.ExecutableResolution{}
		}
		splitCommand := split.List.Pipelines[0].Commands[0]
		if len(splitCommand.Redirects) != 0 {
			return shellsyntax.ExecutableResolution{}
		}

		expanded := make([]shellsyntax.Word, 0, len(words)+len(splitCommand.Words)+1)
		expanded = append(expanded, words[:optionIndex]...)
		if precedingShortOptions != "" {
			expanded = append(expanded, shellsyntax.Word{Value: "-" + precedingShortOptions})
		}
		expanded = append(expanded, splitCommand.Words...)
		expanded = append(expanded, words[remainderStart:]...)
		return resolveUnsupportedDispatch(shellsyntax.SimpleCommand{
			Words:       expanded,
			Assignments: command.Assignments,
		})
	}
	return shellsyntax.ExecutableResolution{}
}

func envShortSplitOperand(words []shellsyntax.Word, optionIndex int) (splitValue, preceding string, remainderStart int, found bool) {
	value := words[optionIndex].Value
	if len(value) < 2 || value[0] != '-' || strings.HasPrefix(value, "--") {
		return "", "", 0, false
	}
	for offset := 1; offset < len(value); offset++ {
		switch value[offset] {
		case 'i', '0', 'v':
			continue
		case 'S':
			if offset+1 < len(value) {
				return value[offset+1:], value[1:offset], optionIndex + 1, true
			}
			if optionIndex+1 >= len(words) {
				return "", "", 0, false
			}
			return words[optionIndex+1].Value, value[1:offset], optionIndex + 2, true
		default:
			// Operand-taking options consume the remainder, so a later S is
			// data rather than another option in the cluster.
			return "", "", 0, false
		}
	}
	return "", "", 0, false
}

// resolveUnsupportedDispatch recovers executable positions for a small set of
// executable-dispatching wrapper forms that the common parser deliberately
// marks unsupported. The original parser issue remains, so non-dangerous forms
// still warn; this recovery exists only to prevent known destructive or
// evaluating executables from being hidden behind those wrappers.
func resolveUnsupportedDispatch(command shellsyntax.SimpleCommand) shellsyntax.ExecutableResolution {
	words := command.Words
	index := len(command.Assignments)

dispatch:
	for index < len(words) {
		if words[index].Dynamic {
			return shellsyntax.ExecutableResolution{}
		}
		switch safetyExecutableBase(words[index].Value) {
		case "command":
			var ok bool
			index, ok = consumeCommandDispatchOptions(words, index+1)
			if !ok {
				return shellsyntax.ExecutableResolution{}
			}
			continue
		case "env":
			var ok bool
			index, ok = consumeEnvDispatchOptions(words, index+1)
			if !ok {
				return shellsyntax.ExecutableResolution{}
			}
			continue dispatch
		default:
			goto resolved
		}
	}

resolved:
	if index >= len(words) {
		return shellsyntax.ExecutableResolution{}
	}
	return shellsyntax.ResolveExecutable(shellsyntax.SimpleCommand{Words: words[index:]})
}

func consumeCommandDispatchOptions(words []shellsyntax.Word, index int) (int, bool) {
	for index < len(words) {
		value := words[index].Value
		if value == "--" {
			return index + 1, true
		}
		if !strings.HasPrefix(value, "-") || value == "-" {
			return index, true
		}
		if len(value) < 2 {
			return 0, false
		}
		for _, option := range value[1:] {
			// Only -p dispatches an executable. -v and -V inspect names and
			// therefore must not cause later data to be classified executable.
			if option != 'p' {
				return 0, false
			}
		}
		index++
	}
	return 0, false
}

func consumeEnvDispatchOptions(words []shellsyntax.Word, index int) (int, bool) {
	optionMode := true
	for index < len(words) {
		value := words[index].Value
		if optionMode && value == "--" {
			optionMode = false
			index++
			continue
		}
		if safetyAssignment(value) {
			index++
			continue
		}
		if !optionMode || !strings.HasPrefix(value, "-") || value == "-" {
			return index, true
		}

		switch {
		case value == "--ignore-environment" || value == "--null" || value == "--debug":
			index++
		case value == "--unset" || value == "--chdir" || value == "--argv0":
			if index+1 >= len(words) {
				return 0, false
			}
			index += 2
		case strings.HasPrefix(value, "--unset=") && len(value) > len("--unset="),
			strings.HasPrefix(value, "--chdir=") && len(value) > len("--chdir="),
			strings.HasPrefix(value, "--argv0=") && len(value) > len("--argv0="):
			index++
		case strings.HasPrefix(value, "--"):
			return 0, false
		default:
			next, ok := consumeEnvShortOptions(words, index)
			if !ok {
				return 0, false
			}
			index = next
		}
	}
	return 0, false
}

func consumeEnvShortOptions(words []shellsyntax.Word, index int) (int, bool) {
	value := words[index].Value
	for offset := 1; offset < len(value); offset++ {
		switch value[offset] {
		case 'i', '0', 'v':
			continue
		case 'u', 'C', 'a', 'P':
			if offset+1 < len(value) {
				return index + 1, true
			}
			if index+1 >= len(words) {
				return 0, false
			}
			return index + 2, true
		case 'S':
			// Split-string rewrites the argument vector, so the executable
			// position cannot be recovered without performing env's parsing.
			return 0, false
		default:
			return 0, false
		}
	}
	return index + 1, true
}

func safetyExecutableBase(value string) string {
	value = strings.TrimRight(value, "/")
	if value == "" {
		return ""
	}
	return path.Base(value)
}

func safetyAssignment(value string) bool {
	equals := strings.IndexByte(value, '=')
	if equals < 1 {
		return false
	}
	for index, r := range value[:equals] {
		if index == 0 {
			if r != '_' && (r < 'A' || r > 'Z') && (r < 'a' || r > 'z') {
				return false
			}
			continue
		}
		if r != '_' && (r < 'A' || r > 'Z') && (r < 'a' || r > 'z') && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}

func classifyRedirects(redirects []shellsyntax.Redirect, add func(Decision, string)) {
	for _, redirect := range redirects {
		if redirect.Duplicate || redirect.CloseFD {
			add(Warn, "Command changes file-descriptor routing.")
			continue
		}
		switch redirect.Operator {
		case shellsyntax.RedirectOutput, shellsyntax.RedirectAppend:
			add(Warn, "Command redirects output and may write to a file.")
		case shellsyntax.RedirectInput:
			add(Warn, "Command reads input through a redirect.")
		default:
			add(Warn, "Command contains an unrecognized redirect.")
		}
	}
}

func classifyIssues(issues []shellsyntax.Issue, add func(Decision, string)) {
	for _, issue := range issues {
		switch issue.Code {
		case "unsupported-command-substitution", "unsupported-process-substitution", "unsupported-shell-evaluation":
			add(Block, "Command contains an unsupported shell evaluation construct.")
		case "empty-command":
			add(Block, "Empty command.")
		case "invalid-utf8":
			add(Warn, "Command contains invalid UTF-8 and cannot be classified reliably.")
		default:
			add(Warn, "Shell syntax is malformed or outside the supported subset: "+issue.Message+".")
		}
	}
}

func aggregate(findings []finding) Result {
	decision := Allow
	for _, item := range findings {
		if severity(item.decision) > severity(decision) {
			decision = item.decision
		}
	}

	var reasons []string
	for _, level := range []Decision{Block, Warn, Allow} {
		for _, item := range findings {
			if item.decision == level {
				reasons = append(reasons, item.reason)
			}
		}
	}
	if decision != Allow {
		// Allow reasons add no useful context once a warning or block exists.
		reasons = slices.DeleteFunc(reasons, func(reason string) bool {
			return reason == "Command appears to be read-only."
		})
	}
	return Result{Decision: decision, Reasons: reasons}
}

func severity(decision Decision) int {
	switch decision {
	case Block:
		return 2
	case Warn:
		return 1
	default:
		return 0
	}
}

func wordValues(words []shellsyntax.Word) []string {
	values := make([]string, len(words))
	for i, word := range words {
		values[i] = word.Value
	}
	return values
}

func startsWithAnyArgument(args []string, values ...string) bool {
	return len(args) > 0 && slices.Contains(values, args[0])
}

func gitCommand(args []string) ([]string, bool) {
	for index := 0; index < len(args); index++ {
		value := args[index]
		if value == "--" {
			if index+1 >= len(args) {
				return nil, false
			}
			return args[index+1:], true
		}
		if !strings.HasPrefix(value, "-") || value == "-" {
			return args[index:], true
		}
		switch {
		case slices.Contains([]string{"-p", "-P", "--no-pager", "--paginate", "--bare", "--no-replace-objects", "--no-lazy-fetch", "--no-optional-locks", "--no-advice", "--literal-pathspecs", "--glob-pathspecs", "--noglob-pathspecs", "--icase-pathspecs"}, value):
			continue
		case value == "-C" || value == "-c" || value == "--git-dir" || value == "--work-tree" || value == "--namespace" || value == "--exec-path" || value == "--config-env" || value == "--attr-source":
			if index+1 >= len(args) {
				return nil, false
			}
			index++
		case strings.HasPrefix(value, "-C") && len(value) > 2,
			strings.HasPrefix(value, "-c") && len(value) > 2,
			strings.HasPrefix(value, "--git-dir="),
			strings.HasPrefix(value, "--work-tree="),
			strings.HasPrefix(value, "--namespace="),
			strings.HasPrefix(value, "--exec-path="),
			strings.HasPrefix(value, "--config-env="),
			strings.HasPrefix(value, "--attr-source="):
			continue
		default:
			return nil, false
		}
	}
	return nil, false
}

func gitDestructive(args []string) bool {
	if len(args) == 0 {
		return false
	}
	return gitSubcommandDestructive(args[0], args[1:])
}

// gitSubcommandDestructive reports whether a resolved git subcommand together
// with its trailing arguments performs a destructive operation. Force flags are
// matched precisely: --force/-f only applies to push, -D (or the equivalent
// --delete --force pair) only to branch, and drop/clear only to stash.
func gitSubcommandDestructive(subcommand string, rest []string) bool {
	switch subcommand {
	case "clean":
		return true
	case "reset":
		return slices.Contains(rest, "--hard")
	case "push":
		return gitPushForced(rest)
	case "branch":
		return gitBranchForceDeleted(rest)
	case "stash":
		return len(rest) > 0 && (rest[0] == "drop" || rest[0] == "clear")
	}
	return false
}

func gitPushForced(rest []string) bool {
	for _, value := range rest {
		if value == "--force" || value == "-f" || value == "--force-with-lease" ||
			strings.HasPrefix(value, "--force-with-lease=") {
			return true
		}
	}
	return false
}

func gitBranchForceDeleted(rest []string) bool {
	if slices.Contains(rest, "-D") {
		return true
	}
	return slices.Contains(rest, "--delete") && slices.Contains(rest, "--force")
}

// gitDestructiveUnderUncertainty prevents a newly introduced or otherwise
// unknown leading Git option from turning a visible destructive subcommand into
// a warning. It is used only after exact global-option resolution failed.
func gitDestructiveUnderUncertainty(args []string) bool {
	for index, value := range args {
		if gitSubcommandDestructive(value, args[index+1:]) {
			return true
		}
	}
	return false
}

func isShell(base string) bool {
	return slices.Contains([]string{"sh", "bash", "zsh", "fish"}, base)
}
