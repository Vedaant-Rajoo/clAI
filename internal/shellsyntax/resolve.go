package shellsyntax

import (
	"path"
	"strings"
)

// Wrapper records a supported command-dispatch wrapper.
type Wrapper struct {
	Name        string
	Word        Word
	Options     []Word
	Assignments []Assignment
}

// ExecutableResolution identifies the effective executable without consulting
// the environment or filesystem.
type ExecutableResolution struct {
	Found              bool
	Word               Word
	Value              string
	Base               string
	Arguments          []Word
	LeadingAssignments []Assignment
	Wrappers           []Wrapper
	ShellEvaluation    bool
	Issues             []Issue
}

// ResolveExecutable unwraps leading assignments and the supported env/command
// forms. Path-qualified names are reduced with path.Base for policy matching;
// no PATH lookup or filesystem access occurs.
func ResolveExecutable(command SimpleCommand) ExecutableResolution {
	resolution := ExecutableResolution{LeadingAssignments: command.Assignments}
	words := command.Words
	index := len(command.Assignments)

	for index < len(words) {
		word := words[index]
		if word.Dynamic {
			resolution.Issues = append(resolution.Issues, Issue{
				Code: "obscured-executable", Kind: Unsupported, Span: word.Span,
				Message: "parameter expansion or substitution obscures an executable position",
			})
			return resolution
		}
		base := executableBase(word.Value)
		switch base {
		case "env":
			wrapper := Wrapper{Name: "env", Word: word}
			index++
			optionMode := true
			assignmentSeen := false
			for index < len(words) {
				candidate := words[index]
				if optionMode && candidate.Value == "--" {
					wrapper.Options = append(wrapper.Options, candidate)
					index++
					optionMode = false
					continue
				}
				if optionMode && candidate.Value == "-i" {
					wrapper.Options = append(wrapper.Options, candidate)
					index++
					continue
				}
				if optionMode && strings.HasPrefix(candidate.Value, "-") {
					resolution.Issues = append(resolution.Issues, Issue{
						Code: "unsupported-env-option", Kind: Unsupported, Span: candidate.Span,
						Message: "env wrapper option is outside the supported subset",
					})
					return resolution
				}
				if name, ok := assignmentName(candidate); ok {
					wrapper.Assignments = append(wrapper.Assignments, Assignment{Name: name, Word: candidate, Span: candidate.Span})
					index++
					optionMode = false
					assignmentSeen = true
					continue
				}
				if assignmentSeen && candidate.Value == "--" {
					wrapper.Options = append(wrapper.Options, candidate)
					index++
					assignmentSeen = false
					continue
				}
				if assignmentSeen && strings.HasPrefix(candidate.Value, "-") {
					resolution.Issues = append(resolution.Issues, Issue{
						Code: "unsupported-env-option", Kind: Unsupported, Span: candidate.Span,
						Message: "env options after assignments are outside the supported subset",
					})
					return resolution
				}
				break
			}
			resolution.Wrappers = append(resolution.Wrappers, wrapper)
		case "command":
			wrapper := Wrapper{Name: "command", Word: word}
			index++
			if index < len(words) && words[index].Value == "--" {
				wrapper.Options = append(wrapper.Options, words[index])
				index++
			} else if index < len(words) && strings.HasPrefix(words[index].Value, "-") {
				resolution.Issues = append(resolution.Issues, Issue{
					Code: "unsupported-command-option", Kind: Unsupported, Span: words[index].Span,
					Message: "command wrapper option is outside the supported subset",
				})
				return resolution
			}
			resolution.Wrappers = append(resolution.Wrappers, wrapper)
		default:
			resolution.Found = true
			resolution.Word = word
			resolution.Value = word.Value
			resolution.Base = base
			if index+1 < len(words) {
				resolution.Arguments = words[index+1:]
			}
			evaluation, uncertainty := analyzeShellInvocation(base, resolution.Arguments)
			if evaluation != nil {
				resolution.ShellEvaluation = true
				resolution.Issues = append(resolution.Issues, Issue{
					Code: "unsupported-shell-evaluation", Kind: Unsupported, Span: evaluation.Span,
					Message: "shell command-string evaluation is outside the supported subset",
				})
			} else if uncertainty != nil {
				resolution.Issues = append(resolution.Issues, *uncertainty)
			}
			return resolution
		}
	}

	if len(resolution.Wrappers) > 0 {
		last := resolution.Wrappers[len(resolution.Wrappers)-1].Word.Span
		resolution.Issues = append(resolution.Issues, Issue{
			Code: "missing-wrapped-executable", Kind: Malformed, Span: last,
			Message: "wrapper is missing an executable",
		})
	}
	return resolution
}

func executableBase(value string) string {
	value = strings.TrimRight(value, "/")
	if value == "" {
		return ""
	}
	return path.Base(value)
}

func analyzeShellInvocation(base string, args []Word) (*Word, *Issue) {
	switch base {
	case "sh", "bash", "zsh", "fish":
	default:
		return nil, nil
	}

	for index := 0; index < len(args); index++ {
		arg := &args[index]
		value := arg.Value
		if value == "--" {
			return nil, nil
		}
		if shellEvaluationOption(base, value) {
			return arg, nil
		}
		if shellOptionHasAttachedOperand(base, value) {
			continue
		}
		if shellOptionRequiresOperand(base, value) {
			if index+1 >= len(args) {
				issue := Issue{
					Code: "malformed-shell-option", Kind: Malformed, Span: arg.Span,
					Message: "shell option is missing its operand",
				}
				return nil, &issue
			}
			index++
			continue
		}
		if !strings.HasPrefix(value, "-") && !strings.HasPrefix(value, "+") {
			return nil, nil
		}
		if knownShellNoOperandOption(base, value) || knownShellFlagCluster(base, value) {
			continue
		}
		issue := Issue{
			Code: "unsupported-shell-option", Kind: Unsupported, Span: arg.Span,
			Message: "shell option cannot be resolved without dialect-specific evaluation",
		}
		return nil, &issue
	}
	return nil, nil
}

func shellEvaluationOption(base, value string) bool {
	if value == "--command" || strings.HasPrefix(value, "--command=") {
		return true
	}
	if base == "fish" && (value == "-C" || strings.HasPrefix(value, "-C") || value == "--init-command" || strings.HasPrefix(value, "--init-command=")) {
		return true
	}
	if !strings.HasPrefix(value, "-") || strings.HasPrefix(value, "--") || len(value) < 2 {
		return false
	}
	if shellOptionHasAttachedOperand(base, value) {
		return false
	}
	return strings.Contains(value[1:], "c")
}

func shellOptionRequiresOperand(base, value string) bool {
	switch base {
	case "bash":
		return value == "-O" || value == "+O" || value == "-o" || value == "+o" || value == "--rcfile" || value == "--init-file"
	case "sh", "zsh":
		return value == "-o" || value == "+o"
	case "fish":
		return value == "-d" || value == "--debug" || value == "--debug-output"
	default:
		return false
	}
}

func shellOptionHasAttachedOperand(base, value string) bool {
	switch base {
	case "bash":
		return attachedShortOperand(value, "-O", "+O", "-o", "+o") || attachedLongOperand(value, "--rcfile", "--init-file")
	case "sh", "zsh":
		return attachedShortOperand(value, "-o", "+o")
	case "fish":
		return attachedShortOperand(value, "-d") || attachedLongOperand(value, "--debug", "--debug-output")
	default:
		return false
	}
}

func attachedShortOperand(value string, prefixes ...string) bool {
	for _, prefix := range prefixes {
		if strings.HasPrefix(value, prefix) && len(value) > len(prefix) {
			return true
		}
	}
	return false
}

func attachedLongOperand(value string, names ...string) bool {
	for _, name := range names {
		if strings.HasPrefix(value, name+"=") {
			return true
		}
	}
	return false
}

func knownShellNoOperandOption(base, value string) bool {
	switch base {
	case "bash":
		switch value {
		case "--noprofile", "--norc", "--posix", "--restricted", "--verbose", "--version", "--login":
			return true
		}
	case "fish":
		switch value {
		case "--no-config", "--private", "--login", "--interactive", "--no-execute":
			return true
		}
	}
	return false
}

func knownShellFlagCluster(base, value string) bool {
	if len(value) < 2 || value[0] != '-' || strings.HasPrefix(value, "--") {
		return false
	}
	allowed := "abefhiklmnprstuvx"
	if base == "fish" {
		allowed = "hilnNv"
	}
	for _, flag := range value[1:] {
		if !strings.ContainsRune(allowed, flag) {
			return false
		}
	}
	return true
}
