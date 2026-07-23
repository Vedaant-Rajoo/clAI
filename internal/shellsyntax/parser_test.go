package shellsyntax

import (
	"reflect"
	"testing"
)

func TestParseSupportedWordsQuotesEscapesAndOperators(t *testing.T) {
	tests := []struct {
		name      string
		source    string
		pipelines int
		commands  []string
		operators []ListOperatorKind
	}{
		{name: "unquoted words", source: "git status --short", pipelines: 1, commands: []string{"git"}},
		{name: "backslash escape", source: `printf hello\ world`, pipelines: 1, commands: []string{"printf"}},
		{name: "single and double quotes", source: `printf '%s\n' "hello world"`, pipelines: 1, commands: []string{"printf"}},
		{name: "empty quoted word", source: `printf '' ""`, pipelines: 1, commands: []string{"printf"}},
		{name: "pipeline", source: "printf x | rg x | wc -l", pipelines: 1, commands: []string{"printf", "rg", "wc"}},
		{name: "compound list", source: "pwd && git status || echo no; date", pipelines: 4, commands: []string{"pwd", "git", "echo", "date"}, operators: []ListOperatorKind{AndIf, OrIf, Sequence}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := Parse(tt.source)
			if !result.Supported() {
				t.Fatalf("Parse(%q) issues = %#v", tt.source, result.Issues)
			}
			if got := len(result.List.Pipelines); got != tt.pipelines {
				t.Fatalf("pipelines = %d, want %d", got, tt.pipelines)
			}
			var executables []string
			for _, pipeline := range result.List.Pipelines {
				for _, command := range pipeline.Commands {
					resolved := ResolveExecutable(command)
					if resolved.Found {
						executables = append(executables, resolved.Base)
					}
				}
			}
			if !reflect.DeepEqual(executables, tt.commands) {
				t.Fatalf("executables = %#v, want %#v", executables, tt.commands)
			}
			var operators []ListOperatorKind
			for _, operator := range result.List.Operators {
				operators = append(operators, operator.Kind)
			}
			if !reflect.DeepEqual(operators, tt.operators) {
				t.Fatalf("operators = %#v, want %#v", operators, tt.operators)
			}
		})
	}
}

func TestParseWordValuesAndOffsets(t *testing.T) {
	source := `printf pre' single '" double "post escaped\ value`
	result := Parse(source)
	if !result.Supported() {
		t.Fatalf("issues = %#v", result.Issues)
	}
	words := result.List.Pipelines[0].Commands[0].Words
	want := []struct {
		value string
		span  Span
	}{
		{value: "printf", span: Span{0, 6}},
		{value: "pre single  double post", span: Span{7, 34}},
		{value: "escaped value", span: Span{35, 49}},
	}
	if len(words) != len(want) {
		t.Fatalf("words = %#v", words)
	}
	for i := range want {
		if words[i].Value != want[i].value || words[i].Span != want[i].span {
			t.Errorf("word[%d] = {%q %#v}, want {%q %#v}", i, words[i].Value, words[i].Span, want[i].value, want[i].span)
		}
		if got := source[words[i].Span.Start:words[i].Span.End]; got == "" {
			t.Errorf("word[%d] has empty source slice", i)
		}
	}
}

func TestParseAssignmentsAndExecutableResolution(t *testing.T) {
	tests := []struct {
		name        string
		source      string
		base        string
		assignments []string
		wrappers    []string
		options     [][]string
	}{
		{name: "leading assignments", source: `FOO=bar EMPTY= QUOTED="two words" /usr/bin/printf ok`, base: "printf", assignments: []string{"FOO", "EMPTY", "QUOTED"}},
		{name: "env assignment", source: `env FOO=bar /bin/rm file`, base: "rm", wrappers: []string{"env"}},
		{name: "env clean option and boundary", source: `env -i FOO=bar -- ./tool arg`, base: "tool", wrappers: []string{"env"}, options: [][]string{{"-i", "--"}}},
		{name: "command wrapper", source: `command rm file`, base: "rm", wrappers: []string{"command"}},
		{name: "command option boundary", source: `command -- /sbin/reboot`, base: "reboot", wrappers: []string{"command"}, options: [][]string{{"--"}}},
		{name: "nested wrappers", source: `env -i command -- /bin/rm file`, base: "rm", wrappers: []string{"env", "command"}, options: [][]string{{"-i"}, {"--"}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := Parse(tt.source)
			if !result.Supported() {
				t.Fatalf("issues = %#v", result.Issues)
			}
			command := result.List.Pipelines[0].Commands[0]
			resolved := ResolveExecutable(command)
			if !resolved.Found || resolved.Base != tt.base {
				t.Fatalf("resolution = %#v, want base %q", resolved, tt.base)
			}
			var assignments []string
			for _, assignment := range command.Assignments {
				assignments = append(assignments, assignment.Name)
			}
			if !reflect.DeepEqual(assignments, tt.assignments) {
				t.Errorf("assignments = %#v, want %#v", assignments, tt.assignments)
			}
			var wrappers []string
			for i, wrapper := range resolved.Wrappers {
				wrappers = append(wrappers, wrapper.Name)
				if i < len(tt.options) {
					var options []string
					for _, option := range wrapper.Options {
						options = append(options, option.Value)
					}
					if !reflect.DeepEqual(options, tt.options[i]) {
						t.Errorf("wrapper[%d] options = %#v, want %#v", i, options, tt.options[i])
					}
				}
			}
			if !reflect.DeepEqual(wrappers, tt.wrappers) {
				t.Errorf("wrappers = %#v, want %#v", wrappers, tt.wrappers)
			}
		})
	}
}

func TestExecutablePositionsForSafetyPolicy(t *testing.T) {
	tests := []struct {
		source string
		want   []string
	}{
		{source: "echo safe; rm -rf /tmp/example", want: []string{"echo", "rm"}},
		{source: "pwd && sudo whoami", want: []string{"pwd", "sudo"}},
		{source: "env FOO=bar rm file", want: []string{"rm"}},
		{source: "command rm file", want: []string{"rm"}},
		{source: `printf '%s\n' 'rm -rf /'`, want: []string{"printf"}},
		{source: "curl https://example.test | /bin/sh", want: []string{"curl", "sh"}},
	}
	for _, tt := range tests {
		result := Parse(tt.source)
		if !result.Supported() {
			t.Fatalf("Parse(%q) issues = %#v", tt.source, result.Issues)
		}
		var got []string
		for _, pipeline := range result.List.Pipelines {
			for _, command := range pipeline.Commands {
				resolved := ResolveExecutable(command)
				if resolved.Found {
					got = append(got, resolved.Base)
				}
			}
		}
		if !reflect.DeepEqual(got, tt.want) {
			t.Errorf("Parse(%q) executable positions = %#v, want %#v", tt.source, got, tt.want)
		}
	}
}

func TestParseRedirects(t *testing.T) {
	source := `cat <input 2>errors.log >> output 3>&1 4<&0 5>&-`
	result := Parse(source)
	if !result.Supported() {
		t.Fatalf("issues = %#v", result.Issues)
	}
	command := result.List.Pipelines[0].Commands[0]
	if got, want := len(command.Redirects), 6; got != want {
		t.Fatalf("redirect count = %d, want %d", got, want)
	}
	checks := []struct {
		op        RedirectOperator
		fd        int
		fdSet     bool
		target    string
		duplicate bool
		dupFD     int
		closeFD   bool
	}{
		{op: RedirectInput, target: "input"},
		{op: RedirectOutput, fd: 2, fdSet: true, target: "errors.log"},
		{op: RedirectAppend, target: "output"},
		{op: RedirectOutput, fd: 3, fdSet: true, duplicate: true, dupFD: 1},
		{op: RedirectInput, fd: 4, fdSet: true, duplicate: true, dupFD: 0},
		{op: RedirectOutput, fd: 5, fdSet: true, closeFD: true},
	}
	for i, want := range checks {
		got := command.Redirects[i]
		target := ""
		if got.Target != nil {
			target = got.Target.Value
		}
		if got.Operator != want.op || got.FD != want.fd || got.FDSet != want.fdSet || target != want.target || got.Duplicate != want.duplicate || got.DuplicateFD != want.dupFD || got.CloseFD != want.closeFD {
			t.Errorf("redirect[%d] = %#v target %q, want %#v", i, got, target, want)
		}
	}
	resolved := ResolveExecutable(command)
	if !resolved.Found || resolved.Base != "cat" {
		t.Fatalf("resolution = %#v", resolved)
	}
}

func TestReservedControlSyntax(t *testing.T) {
	reserved := []string{
		"!", "if", "then", "else", "elif", "fi", "for", "while", "until", "do", "done",
		"case", "esac", "function", "select", "in", "time", "coproc",
		"repeat", "foreach", "end", "always", "nocorrect", "noglob",
		"begin", "switch", "and", "or", "not", "break", "continue", "return", "builtin", "exec",
		"declare", "export", "float", "integer", "local", "readonly", "typeset", "{", "}", "[[", "]]",
	}
	for _, word := range reserved {
		t.Run(word, func(t *testing.T) {
			result := Parse(word + " argument")
			if !hasIssue(result, "unsupported-control-syntax") {
				t.Fatalf("Parse(%q) issues = %#v, want unsupported-control-syntax", word+" argument", result.Issues)
			}
			issue := issueByCode(result, "unsupported-control-syntax")
			if issue == nil || issue.Span != (Span{0, len(word)}) {
				t.Fatalf("control issue = %#v, want span %#v", issue, Span{0, len(word)})
			}
		})
	}

	for _, word := range reserved {
		for _, source := range []string{`"` + word + `" argument`, `\` + word + ` argument`, `echo ` + word} {
			t.Run("data "+source, func(t *testing.T) {
				result := Parse(source)
				if !result.Supported() {
					t.Fatalf("Parse(%q) issues = %#v; quoted, escaped, and later argument occurrences are data", source, result.Issues)
				}
			})
		}
	}
	for _, source := range []string{`echo '{'`, `echo \}`} {
		result := Parse(source)
		if !result.Supported() {
			t.Fatalf("Parse(%q) issues = %#v; quoted and escaped braces are data", source, result.Issues)
		}
	}
}

func TestShellEvaluationDetection(t *testing.T) {
	tests := []struct {
		name   string
		source string
		code   string
		eval   bool
	}{
		{name: "operand option before c", source: `bash -O extglob -c 'echo x'`, code: "unsupported-shell-evaluation", eval: true},
		{name: "attached operand option before cluster", source: `bash -Oextglob -lc 'echo x'`, code: "unsupported-shell-evaluation", eval: true},
		{name: "path qualified shell", source: `/usr/bin/bash -o posix -c 'echo x'`, code: "unsupported-shell-evaluation", eval: true},
		{name: "bash noprofile before c", source: `bash --noprofile -c 'echo x'`, code: "unsupported-shell-evaluation", eval: true},
		{name: "bash norc before cluster", source: `/bin/bash --norc -lc 'echo x'`, code: "unsupported-shell-evaluation", eval: true},
		{name: "bash posix and restricted before c", source: `bash --posix --restricted -c 'echo x'`, code: "unsupported-shell-evaluation", eval: true},
		{name: "bash verbose login before c", source: `bash --verbose --login -c 'echo x'`, code: "unsupported-shell-evaluation", eval: true},
		{name: "bash version before c conservative", source: `bash --version -c 'echo x'`, code: "unsupported-shell-evaluation", eval: true},
		{name: "fish attached long command", source: `fish --command=echo`, code: "unsupported-shell-evaluation", eval: true},
		{name: "fish init command", source: `fish --init-command=echo`, code: "unsupported-shell-evaluation", eval: true},
		{name: "nested wrappers", source: `env -i command -- /bin/zsh -o EXTENDED_GLOB -lc 'echo x'`, code: "unsupported-shell-evaluation", eval: true},
		{name: "option termination", source: `bash -- -c script`, eval: false},
		{name: "missing option operand", source: `bash -O`, code: "malformed-shell-option", eval: false},
		{name: "unknown option conservative", source: `bash --mystery value -c command`, code: "unsupported-shell-option", eval: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := Parse(tt.source)
			command := result.List.Pipelines[0].Commands[0]
			resolved := ResolveExecutable(command)
			if resolved.ShellEvaluation != tt.eval {
				t.Errorf("ResolveExecutable(%q).ShellEvaluation = %v, want %v; %#v", tt.source, resolved.ShellEvaluation, tt.eval, resolved)
			}
			if tt.code == "" {
				if !result.Supported() {
					t.Fatalf("Parse(%q) issues = %#v", tt.source, result.Issues)
				}
			} else if !hasIssue(result, tt.code) {
				t.Fatalf("Parse(%q) issues = %#v, want %q", tt.source, result.Issues, tt.code)
			}
		})
	}
}

func TestRedirectDescriptorPrefixRequiresTokenBoundary(t *testing.T) {
	tests := []struct {
		name   string
		source string
		word   string
		fdSet  bool
		fd     int
	}{
		{name: "digits in executable", source: `safe2>out`, word: "safe2"},
		{name: "descriptor at boundary", source: `safe 2>out`, word: "safe", fdSet: true, fd: 2},
		{name: "escaped digit in executable", source: `safe\2>out`, word: "safe2"},
		{name: "quoted executable", source: `"safe2">out`, word: "safe2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := Parse(tt.source)
			if !result.Supported() {
				t.Fatalf("Parse(%q) issues = %#v", tt.source, result.Issues)
			}
			command := result.List.Pipelines[0].Commands[0]
			resolved := ResolveExecutable(command)
			if !resolved.Found || resolved.Value != tt.word {
				t.Fatalf("resolution = %#v, want executable %q", resolved, tt.word)
			}
			if len(command.Redirects) != 1 {
				t.Fatalf("redirects = %#v", command.Redirects)
			}
			redirect := command.Redirects[0]
			if redirect.FDSet != tt.fdSet || redirect.FD != tt.fd {
				t.Errorf("redirect descriptor = set:%v fd:%d, want set:%v fd:%d", redirect.FDSet, redirect.FD, tt.fdSet, tt.fd)
			}
			if redirect.Target == nil || redirect.Target.Value != "out" {
				t.Errorf("redirect target = %#v, want out", redirect.Target)
			}
		})
	}
}

func TestFDRedirectCloseOperandBoundary(t *testing.T) {
	source := `echo 2>&-oops`
	result := Parse(source)
	issue := issueByCode(result, "malformed-fd-redirect")
	if issue == nil {
		t.Fatalf("issues = %#v, want malformed-fd-redirect", result.Issues)
	}
	if issue.Span != (Span{8, 13}) {
		t.Fatalf("malformed close span = %#v, want %#v", issue.Span, Span{8, 13})
	}
	redirect := result.List.Pipelines[0].Commands[0].Redirects[0]
	if !redirect.CloseFD || redirect.Span != (Span{5, 13}) {
		t.Fatalf("redirect = %#v, want closed descriptor spanning entire malformed token", redirect)
	}
}

func TestInvalidUTF8AndAdjacentMalformedSyntax(t *testing.T) {
	invalid := string([]byte{'e', 'c', 'h', 'o', ' ', 0xff})
	result := Parse(invalid)
	issue := issueByCode(result, "invalid-utf8")
	if issue == nil || issue.Span != (Span{5, 6}) {
		t.Fatalf("invalid UTF-8 issues = %#v, want byte span %#v", result.Issues, Span{5, 6})
	}

	adjacent := Parse(`echo&&&rm`)
	issue = issueByCode(adjacent, "unsupported-background")
	if issue == nil || issue.Span != (Span{6, 7}) {
		t.Fatalf("adjacent malformed issues = %#v, want background span %#v", adjacent.Issues, Span{6, 7})
	}
}

func TestParseMalformedAndUnsupportedIssues(t *testing.T) {
	tests := []struct {
		name   string
		source string
		codes  []string
	}{
		{name: "empty", source: "   ", codes: []string{"empty-command"}},
		{name: "leading separator", source: "; echo ok", codes: []string{"empty-list-segment"}},
		{name: "trailing separator", source: "echo ok;", codes: []string{"trailing-operator"}},
		{name: "empty conditional segment", source: "echo a && || echo b", codes: []string{"empty-list-segment"}},
		{name: "leading pipe", source: "| echo ok", codes: []string{"empty-pipeline-command"}},
		{name: "trailing pipe", source: "echo ok |", codes: []string{"trailing-operator"}},
		{name: "missing redirect target", source: "echo ok >", codes: []string{"missing-redirect-target"}},
		{name: "unclosed single quote", source: "echo 'oops", codes: []string{"unclosed-single-quote"}},
		{name: "unclosed double quote", source: `echo "oops`, codes: []string{"unclosed-double-quote"}},
		{name: "trailing escape", source: "echo oops\\", codes: []string{"trailing-escape"}},
		{name: "record separator", source: "echo one\necho two", codes: []string{"unsupported-record-separator"}},
		{name: "non-common double quote escape", source: `echo "a\q"`, codes: []string{"unsupported-double-quote-escape"}},
		{name: "background", source: "echo ok &", codes: []string{"unsupported-background"}},
		{name: "subshell grouping", source: "(echo ok)", codes: []string{"unsupported-grouping", "unsupported-grouping"}},
		{name: "brace grouping", source: "{ echo ok; }", codes: []string{"unsupported-control-syntax", "unsupported-control-syntax"}},
		{name: "attached opening brace", source: "{cmd", codes: []string{"unsupported-grouping"}},
		{name: "attached closing brace", source: "echo value}", codes: []string{"unsupported-grouping"}},
		{name: "command substitution", source: "echo $(rm file)", codes: []string{"unsupported-command-substitution"}},
		{name: "backtick substitution", source: "echo `rm file`", codes: []string{"unsupported-command-substitution"}},
		{name: "process substitution", source: "cat <(printf x)", codes: []string{"unsupported-process-substitution"}},
		{name: "parameter expansion argument", source: "echo $HOME", codes: []string{"unsupported-parameter-expansion"}},
		{name: "parameter obscured executable", source: "$COMMAND file", codes: []string{"unsupported-parameter-expansion", "obscured-executable"}},
		{name: "comment", source: "echo ok # comment", codes: []string{"unsupported-comment"}},
		{name: "heredoc", source: "cat <<EOF", codes: []string{"unsupported-here-document"}},
		{name: "here string", source: "cat <<<value", codes: []string{"unsupported-here-string"}},
		{name: "shell c", source: `sh -c 'rm file'`, codes: []string{"unsupported-shell-evaluation"}},
		{name: "shell clustered c", source: `bash -lc 'rm file'`, codes: []string{"unsupported-shell-evaluation"}},
		{name: "unsupported env option", source: "env -u FOO rm file", codes: []string{"unsupported-env-option"}},
		{name: "env option after assignment", source: "env FOO=bar -i rm file", codes: []string{"unsupported-env-option"}},
		{name: "unsupported command option", source: "command -v rm", codes: []string{"unsupported-command-option"}},
		{name: "missing wrapped executable", source: "env -i", codes: []string{"missing-wrapped-executable"}},
		{name: "malformed descriptor operand", source: "echo ok 2>&1file", codes: []string{"malformed-fd-redirect"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			first := Parse(tt.source)
			second := Parse(tt.source)
			if !reflect.DeepEqual(first.Issues, second.Issues) {
				t.Fatalf("issues are not deterministic:\nfirst %#v\nsecond %#v", first.Issues, second.Issues)
			}
			var codes []string
			for _, issue := range first.Issues {
				codes = append(codes, issue.Code)
				if issue.Span.Start < 0 || issue.Span.End < issue.Span.Start || issue.Span.End > len(tt.source) {
					t.Errorf("invalid issue span %#v for source length %d", issue.Span, len(tt.source))
				}
			}
			if !containsCodesInOrder(codes, tt.codes) {
				t.Fatalf("issue codes = %#v, want ordered subset %#v; issues = %#v", codes, tt.codes, first.Issues)
			}
			if first.Supported() {
				t.Fatal("unsupported or malformed source reported Supported")
			}
		})
	}
}

func TestIssueOffsets(t *testing.T) {
	tests := []struct {
		source string
		code   string
		span   Span
	}{
		{source: "echo ok &", code: "unsupported-background", span: Span{8, 9}},
		{source: "echo $(rm)", code: "unsupported-command-substitution", span: Span{5, 10}},
		{source: "echo $CMD", code: "unsupported-parameter-expansion", span: Span{5, 9}},
		{source: "cat <<<x", code: "unsupported-here-string", span: Span{4, 7}},
		{source: "echo 'x", code: "unclosed-single-quote", span: Span{5, 7}},
	}
	for _, tt := range tests {
		result := Parse(tt.source)
		var found *Issue
		for i := range result.Issues {
			if result.Issues[i].Code == tt.code {
				found = &result.Issues[i]
				break
			}
		}
		if found == nil {
			t.Errorf("Parse(%q) missing issue %q: %#v", tt.source, tt.code, result.Issues)
			continue
		}
		if found.Span != tt.span {
			t.Errorf("Parse(%q) issue %q span = %#v, want %#v", tt.source, tt.code, found.Span, tt.span)
		}
	}
}

func hasIssue(result Result, code string) bool {
	return issueByCode(result, code) != nil
}

func issueByCode(result Result, code string) *Issue {
	for i := range result.Issues {
		if result.Issues[i].Code == code {
			return &result.Issues[i]
		}
	}
	return nil
}

func containsCodesInOrder(got, want []string) bool {
	index := 0
	for _, code := range got {
		if index < len(want) && code == want[index] {
			index++
		}
	}
	return index == len(want)
}
