package safety

import (
	"strings"

	"github.com/Vedaant-Rajoo/clai/internal/shellsyntax"
)

const maxDispatcherDepth = 16

type dispatcherOutcome struct {
	recognized bool
	noOp       bool
	uncertain  bool
	resolved   shellsyntax.ExecutableResolution
}

func resolvePassThroughDispatcher(resolved shellsyntax.ExecutableResolution) dispatcherOutcome {
	var command []shellsyntax.Word
	var recognized, noOp bool

	switch resolved.Base {
	case "xargs":
		command, noOp = xargsCommand(resolved.Arguments)
		recognized = true
	case "timeout":
		command, noOp = timeoutCommand(resolved.Arguments)
		recognized = true
	case "nice":
		command, noOp = niceCommand(resolved.Arguments)
		recognized = true
	case "nohup":
		command, noOp = nohupCommand(resolved.Arguments)
		recognized = true
	case "stdbuf":
		command, noOp = stdbufCommand(resolved.Arguments)
		recognized = true
	case "setsid":
		command, noOp = setsidCommand(resolved.Arguments)
		recognized = true
	}

	outcome := dispatcherOutcome{recognized: recognized, noOp: noOp}
	if !recognized || noOp || len(command) == 0 {
		return outcome
	}
	outcome.resolved = resolveExecutable(shellsyntax.SimpleCommand{Words: command})
	if outcome.resolved.Found && strings.Contains(outcome.resolved.Word.Value, "{}") {
		outcome.uncertain = true
	}
	return outcome
}

func xargsCommand(args []shellsyntax.Word) ([]shellsyntax.Word, bool) {
	for index := 0; index < len(args); index++ {
		value := args[index].Value
		if args[index].Dynamic {
			return nil, false
		}
		switch {
		case value == "--":
			return defaultXargsCommand(args[index+1:]), false
		case !strings.HasPrefix(value, "-") || value == "-":
			return args[index:], false
		case value == "--help" || value == "--version" || value == "--show-limits":
			return nil, true
		case xargsLongFlag(value):
			continue
		case xargsLongOptionAttached(value):
			continue
		case xargsLongOption(value):
			if index+1 >= len(args) || args[index+1].Dynamic {
				return nil, false
			}
			index++
		default:
			next, ok := consumeXargsShortOption(args, index)
			if !ok {
				return nil, false
			}
			index = next - 1
		}
	}
	return defaultXargsCommand(nil), false
}

func defaultXargsCommand(command []shellsyntax.Word) []shellsyntax.Word {
	if len(command) > 0 {
		return command
	}
	return []shellsyntax.Word{{Value: "echo"}}
}

func xargsLongFlag(value string) bool {
	switch value {
	case "--null", "--interactive", "--open-tty", "--no-run-if-empty", "--verbose", "--exit", "--replace", "--eof":
		return true
	default:
		return false
	}
}

func xargsLongOption(value string) bool {
	switch value {
	case "--arg-file", "--delimiter", "--max-lines", "--max-args", "--max-procs", "--max-chars", "--process-slot-var":
		return true
	default:
		return false
	}
}

func xargsLongOptionAttached(value string) bool {
	for _, prefix := range []string{"--arg-file=", "--delimiter=", "--eof=", "--replace=", "--max-lines=", "--max-args=", "--max-procs=", "--max-chars=", "--process-slot-var="} {
		if strings.HasPrefix(value, prefix) && len(value) > len(prefix) {
			return true
		}
	}
	return false
}

func consumeXargsShortOption(args []shellsyntax.Word, index int) (int, bool) {
	value := args[index].Value
	if len(value) < 2 || value[0] != '-' || strings.HasPrefix(value, "--") {
		return 0, false
	}
	for offset := 1; offset < len(value); offset++ {
		switch value[offset] {
		case '0', 'p', 'o', 'r', 't', 'x':
			continue
		case 'e', 'i', 'l':
			return index + 1, true
		case 'a', 'd', 'E', 'I', 'J', 'L', 'n', 'P', 'R', 'S', 's':
			if offset+1 < len(value) {
				return index + 1, true
			}
			if index+1 >= len(args) || args[index+1].Dynamic {
				return 0, false
			}
			return index + 2, true
		default:
			return 0, false
		}
	}
	return index + 1, true
}

func timeoutCommand(args []shellsyntax.Word) ([]shellsyntax.Word, bool) {
	index := 0
	for index < len(args) {
		value := args[index].Value
		if args[index].Dynamic {
			return nil, false
		}
		switch {
		case value == "--":
			index++
			goto duration
		case value == "--help" || value == "--version":
			return nil, true
		case value == "--preserve-status" || value == "--foreground" || value == "-v" || value == "--verbose":
			index++
		case value == "-k" || value == "--kill-after" || value == "-s" || value == "--signal":
			if index+1 >= len(args) || args[index+1].Dynamic {
				return nil, false
			}
			index += 2
		case strings.HasPrefix(value, "--kill-after=") || strings.HasPrefix(value, "--signal=") ||
			(strings.HasPrefix(value, "-k") || strings.HasPrefix(value, "-s")) && len(value) > 2:
			index++
		case strings.HasPrefix(value, "-"):
			return nil, false
		default:
			goto duration
		}
	}

duration:
	if index+1 >= len(args) || args[index].Dynamic {
		return nil, false
	}
	return args[index+1:], false
}

func niceCommand(args []shellsyntax.Word) ([]shellsyntax.Word, bool) {
	for index := 0; index < len(args); index++ {
		value := args[index].Value
		if args[index].Dynamic {
			return nil, false
		}
		switch {
		case value == "--":
			return args[index+1:], false
		case value == "--help" || value == "--version":
			return nil, true
		case value == "-n" || value == "--adjustment":
			if index+1 >= len(args) || args[index+1].Dynamic {
				return nil, false
			}
			index++
		case strings.HasPrefix(value, "--adjustment=") || strings.HasPrefix(value, "-n") && len(value) > 2 || signedDecimalOption(value):
			continue
		case strings.HasPrefix(value, "-"):
			return nil, false
		default:
			return args[index:], false
		}
	}
	return nil, false
}

func signedDecimalOption(value string) bool {
	if len(value) < 2 || value[0] != '-' {
		return false
	}
	for _, char := range value[1:] {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

func nohupCommand(args []shellsyntax.Word) ([]shellsyntax.Word, bool) {
	if len(args) == 0 || args[0].Dynamic {
		return nil, false
	}
	if args[0].Value == "--help" || args[0].Value == "--version" {
		return nil, true
	}
	if args[0].Value == "--" {
		return args[1:], false
	}
	if strings.HasPrefix(args[0].Value, "-") {
		return nil, false
	}
	return args, false
}

func stdbufCommand(args []shellsyntax.Word) ([]shellsyntax.Word, bool) {
	for index := 0; index < len(args); index++ {
		value := args[index].Value
		if args[index].Dynamic {
			return nil, false
		}
		switch {
		case value == "--":
			return args[index+1:], false
		case value == "--help" || value == "--version":
			return nil, true
		case value == "-i" || value == "-o" || value == "-e" || value == "--input" || value == "--output" || value == "--error":
			if index+1 >= len(args) || args[index+1].Dynamic {
				return nil, false
			}
			index++
		case stdbufAttachedOption(value):
			continue
		case strings.HasPrefix(value, "-"):
			return nil, false
		default:
			return args[index:], false
		}
	}
	return nil, false
}

func stdbufAttachedOption(value string) bool {
	for _, prefix := range []string{"-i", "-o", "-e", "--input=", "--output=", "--error="} {
		if strings.HasPrefix(value, prefix) && len(value) > len(prefix) {
			return true
		}
	}
	return false
}

func setsidCommand(args []shellsyntax.Word) ([]shellsyntax.Word, bool) {
	for index := 0; index < len(args); index++ {
		value := args[index].Value
		if args[index].Dynamic {
			return nil, false
		}
		switch {
		case value == "--":
			return args[index+1:], false
		case value == "--help" || value == "--version" || value == "-h" || value == "-V":
			return nil, true
		case value == "--ctty" || value == "--fork" || value == "--wait" || shortFlagCluster(value, "cfw"):
			continue
		case strings.HasPrefix(value, "-"):
			return nil, false
		default:
			return args[index:], false
		}
	}
	return nil, false
}
