package safety

import (
	"slices"
	"strings"

	"github.com/Vedaant-Rajoo/clai/internal/shellsyntax"
)

func findMayModify(args []string) bool {
	for _, value := range args {
		if slices.Contains([]string{"-delete", "-fls", "-fprint", "-fprint0", "-fprintf"}, value) {
			return true
		}
	}
	return false
}

func classifyGo(args []string, add func(Decision, string)) {
	command, rest, found := goCommand(args)
	if !found {
		add(Warn, "Go command could not be classified as read-only.")
		return
	}

	switch command {
	case "env":
		if slices.ContainsFunc(rest, func(value string) bool {
			return value == "-w" || value == "-u" || strings.HasPrefix(value, "-w=") || strings.HasPrefix(value, "-u=")
		}) {
			add(Warn, "Go env command may persistently change Go environment settings.")
			return
		}
		add(Allow, "Command appears to be read-only.")
	case "list", "version", "doc":
		add(Allow, "Command appears to be read-only.")
	case "vet":
		if slices.ContainsFunc(rest, func(value string) bool {
			return value == "-vettool" || strings.HasPrefix(value, "-vettool=")
		}) {
			add(Warn, "Go vet may dispatch a custom analysis tool.")
			return
		}
		add(Allow, "Command appears to be read-only.")
	case "test":
		add(Warn, "Go test compiles and runs the project's test code.")
	default:
		add(Warn, "Go "+command+" may execute code or modify module and build state.")
	}
}

func goCommand(args []string) (string, []string, bool) {
	for index := 0; index < len(args); index++ {
		value := args[index]
		switch {
		case value == "-C":
			if index+1 >= len(args) {
				return "", nil, false
			}
			index++
		case strings.HasPrefix(value, "-C=") && len(value) > len("-C="),
			strings.HasPrefix(value, "-C") && len(value) > len("-C"):
			continue
		case strings.HasPrefix(value, "-"):
			return "", nil, false
		default:
			return value, args[index+1:], true
		}
	}
	return "", nil, false
}

func gitReadOnly(args []string) bool {
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "status":
		return true
	case "diff", "log":
		return !gitOutputOrExternalDiff(args[1:])
	case "branch":
		return gitBranchReadOnly(args[1:])
	case "remote":
		return gitRemoteReadOnly(args[1:])
	case "tag":
		return gitTagReadOnly(args[1:])
	default:
		return false
	}
}

func gitOutputOrExternalDiff(args []string) bool {
	for _, value := range args {
		switch {
		case value == "--output", strings.HasPrefix(value, "--output="), value == "--ext-diff", value == "--textconv":
			return true
		}
	}
	return false
}

func gitBranchReadOnly(args []string) bool {
	if len(args) == 0 {
		return true
	}

	listMode := false
	for index := 0; index < len(args); index++ {
		value := args[index]
		switch {
		case value == "--":
			return listMode
		case branchMutationOption(value):
			return false
		case value == "--list" || value == "-l":
			listMode = true
		case value == "--show-current":
			if len(args) != 1 {
				return false
			}
			return true
		case branchDisplayFlag(value):
			continue
		case branchDisplayOptionHasAttachedValue(value):
			listMode = true
		case branchDisplayOptionNeedsValue(value):
			if index+1 >= len(args) {
				return false
			}
			index++
			listMode = true
		case strings.HasPrefix(value, "-"):
			return false
		default:
			return listMode
		}
	}
	return true
}

func branchMutationOption(value string) bool {
	switch value {
	case "-d", "-D", "--delete", "-m", "-M", "--move", "-c", "-C", "--copy", "-f", "--force",
		"--edit-description", "--set-upstream-to", "-u", "--unset-upstream", "--track", "--no-track",
		"--recurse-submodules":
		return true
	}
	return strings.HasPrefix(value, "--set-upstream-to=") || strings.HasPrefix(value, "--track=")
}

func branchDisplayFlag(value string) bool {
	switch value {
	case "-a", "--all", "-r", "--remotes", "-v", "-vv", "--verbose", "--no-abbrev", "--ignore-case", "-i",
		"--omit-empty", "--color", "--no-color", "--column", "--no-column":
		return true
	default:
		return shortFlagCluster(value, "arvil")
	}
}

func branchDisplayOptionHasAttachedValue(value string) bool {
	for _, prefix := range []string{"--color=", "--column=", "--sort=", "--format=", "--contains=", "--no-contains=", "--merged=", "--no-merged=", "--points-at=", "--abbrev="} {
		if strings.HasPrefix(value, prefix) && len(value) > len(prefix) {
			return true
		}
	}
	return false
}

func branchDisplayOptionNeedsValue(value string) bool {
	return slices.Contains([]string{"--sort", "--format", "--contains", "--no-contains", "--merged", "--no-merged", "--points-at", "--abbrev"}, value)
}

func gitRemoteReadOnly(args []string) bool {
	if len(args) == 0 {
		return true
	}
	if slices.Equal(args, []string{"-v"}) || slices.Equal(args, []string{"--verbose"}) {
		return true
	}

	switch args[0] {
	case "get-url":
		for _, value := range args[1:] {
			if strings.HasPrefix(value, "-") && value != "--push" && value != "--all" {
				return false
			}
		}
		return len(args) >= 2
	case "show":
		for _, value := range args[1:] {
			if strings.HasPrefix(value, "-") && value != "-n" && value != "--no-query" {
				return false
			}
		}
		return len(args) >= 2
	default:
		return false
	}
}

func gitTagReadOnly(args []string) bool {
	if len(args) == 0 {
		return true
	}

	listMode := false
	for index := 0; index < len(args); index++ {
		value := args[index]
		switch {
		case value == "--":
			return listMode
		case tagMutationOption(value):
			return false
		case value == "--list" || value == "-l":
			listMode = true
		case tagDisplayFlag(value):
			listMode = true
		case tagDisplayOptionHasAttachedValue(value):
			listMode = true
		case tagDisplayOptionNeedsValue(value):
			if index+1 >= len(args) {
				return false
			}
			index++
			listMode = true
		case strings.HasPrefix(value, "-"):
			return false
		default:
			return listMode
		}
	}
	return listMode
}

func tagMutationOption(value string) bool {
	switch value {
	case "-d", "--delete", "-f", "--force", "-a", "--annotate", "-s", "--sign", "-u", "--local-user", "-m", "--message",
		"-F", "--file", "--cleanup", "--create-reflog":
		return true
	}
	for _, prefix := range []string{"--local-user=", "--message=", "--file=", "--cleanup="} {
		if strings.HasPrefix(value, prefix) {
			return true
		}
	}
	return false
}

func tagDisplayFlag(value string) bool {
	return slices.Contains([]string{"-n", "-v", "--verify", "--ignore-case", "-i", "--column", "--no-column", "--color", "--no-color", "--omit-empty"}, value)
}

func tagDisplayOptionHasAttachedValue(value string) bool {
	for _, prefix := range []string{"--contains=", "--no-contains=", "--merged=", "--no-merged=", "--points-at=", "--sort=", "--format=", "--color=", "--column="} {
		if strings.HasPrefix(value, prefix) && len(value) > len(prefix) {
			return true
		}
	}
	return false
}

func tagDisplayOptionNeedsValue(value string) bool {
	return slices.Contains([]string{"--contains", "--no-contains", "--merged", "--no-merged", "--points-at", "--sort", "--format"}, value)
}

func dateReadOnly(args []string) bool {
	if len(args) == 0 {
		return true
	}

	noSet := false
	for index := 0; index < len(args); index++ {
		value := args[index]
		switch {
		case value == "-s" || value == "--set" || strings.HasPrefix(value, "--set="):
			return false
		case value == "-j":
			noSet = true
		case slices.Contains([]string{"-u", "--utc", "--universal", "-R", "--rfc-email", "--debug", "--resolution"}, value):
			continue
		case slices.Contains([]string{"-I", "-Idate", "-Ihours", "-Iminutes", "-Iseconds", "-Ins"}, value) ||
			value == "--iso-8601" || strings.HasPrefix(value, "--iso-8601="):
			continue
		case slices.Contains([]string{"-d", "--date", "-f", "--file", "-r", "--reference", "-v"}, value):
			if index+1 >= len(args) {
				return false
			}
			index++
		case strings.HasPrefix(value, "--date=") || strings.HasPrefix(value, "--file=") || strings.HasPrefix(value, "--reference=") ||
			(strings.HasPrefix(value, "-v") && len(value) > 2):
			continue
		case strings.HasPrefix(value, "+"):
			continue
		case strings.HasPrefix(value, "-"):
			return false
		default:
			if !noSet {
				return false
			}
		}
	}
	return true
}

func hostnameReadOnly(args []string) bool {
	for _, value := range args {
		if !slices.Contains([]string{"-a", "--alias", "-A", "--all-fqdns", "-d", "--domain", "-f", "--fqdn", "--long", "-i", "--ip-address", "-I", "--all-ip-addresses", "-s", "--short", "-y", "--yp", "--nis", "-V", "--version", "--help"}, value) {
			return false
		}
	}
	return true
}

func ifconfigReadOnly(args []string) bool {
	interfaceSeen := false
	for _, value := range args {
		if value == "up" || value == "down" {
			return false
		}
		if slices.Contains([]string{"-a", "-l", "-s", "-u", "-d", "-v", "--help", "--version"}, value) {
			continue
		}
		if strings.HasPrefix(value, "-") || interfaceSeen {
			return false
		}
		interfaceSeen = true
	}
	return true
}

func sortMayWriteOrDispatch(args []string) bool {
	for _, value := range args {
		switch {
		case value == "-o", strings.HasPrefix(value, "-o") && len(value) > 2,
			value == "--output", strings.HasPrefix(value, "--output="),
			value == "--compress-program", strings.HasPrefix(value, "--compress-program="):
			return true
		}
	}
	return false
}

func printfMayAssign(args []string) bool {
	for _, value := range args {
		if value == "-v" || strings.HasPrefix(value, "-v") && len(value) > 2 {
			return true
		}
	}
	return false
}

func shortFlagCluster(value, allowed string) bool {
	if len(value) < 2 || value[0] != '-' || strings.HasPrefix(value, "--") {
		return false
	}
	for _, flag := range value[1:] {
		if !strings.ContainsRune(allowed, flag) {
			return false
		}
	}
	return true
}

func classifyRipgrep(args []shellsyntax.Word, commandIndex, commandCount, depth int, add func(Decision, string)) {
	pre, found, valid := ripgrepPreprocessor(args)
	if !found {
		add(Allow, "Command appears to be read-only.")
		return
	}
	if !valid {
		add(Warn, "Rg --pre dispatches an external preprocessor that could not be resolved safely.")
		return
	}

	resolved := resolveCommandString(pre)
	if !resolved.Found {
		add(Warn, "Rg --pre dispatches an external preprocessor that could not be resolved safely.")
		return
	}
	add(Warn, "Rg --pre dispatches an external preprocessor: "+resolved.Base+".")
	if depth >= maxDispatcherDepth {
		add(Warn, "Command dispatcher nesting exceeds the safety analysis limit.")
		return
	}
	classifyExecutableDepth(resolved, commandIndex, commandCount, depth+1, add)
}

func ripgrepPreprocessor(args []shellsyntax.Word) (string, bool, bool) {
	for index, arg := range args {
		value := arg.Value
		switch {
		case value == "--":
			return "", false, false
		case value == "--pre":
			if index+1 >= len(args) || args[index+1].Dynamic {
				return "", true, false
			}
			return args[index+1].Value, true, args[index+1].Value != ""
		case strings.HasPrefix(value, "--pre="):
			return strings.TrimPrefix(value, "--pre="), true, !arg.Dynamic && len(value) > len("--pre=")
		}
	}
	return "", false, false
}

func resolveCommandString(command string) shellsyntax.ExecutableResolution {
	parsed := shellsyntax.Parse(command)
	if len(parsed.Issues) != 0 || len(parsed.List.Pipelines) != 1 || len(parsed.List.Pipelines[0].Commands) != 1 {
		return shellsyntax.ExecutableResolution{}
	}
	simple := parsed.List.Pipelines[0].Commands[0]
	if len(simple.Redirects) != 0 {
		return shellsyntax.ExecutableResolution{}
	}
	return resolveExecutable(simple)
}
