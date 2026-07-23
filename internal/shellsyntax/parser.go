package shellsyntax

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

type tokenKind uint8

const (
	tokenWord tokenKind = iota
	tokenPipe
	tokenList
	tokenRedirect
)

type token struct {
	kind     tokenKind
	span     Span
	word     Word
	listKind ListOperatorKind
	redirect Redirect
}

// Parse parses source without executing a shell, expanding variables, or
// evaluating substitutions. Unsupported constructs are retained as issues.
func Parse(source string) Result {
	l := lexer{source: source}
	l.recordInvalidUTF8()
	tokens := l.lex()
	r := Result{Source: source, Issues: l.issues}
	r.List = parseTokens(tokens, &r.Issues)
	if len(source) > 0 {
		r.List.Span = Span{Start: 0, End: len(source)}
	}
	appendStructuralIssues(&r)
	return r
}

type lexer struct {
	source string
	pos    int
	issues []Issue
}

func (l *lexer) recordInvalidUTF8() {
	for pos := 0; pos < len(l.source); {
		r, size := utf8.DecodeRuneInString(l.source[pos:])
		if r == utf8.RuneError && size == 1 {
			l.issue("invalid-utf8", Malformed, Span{pos, pos + 1}, "command contains invalid UTF-8")
		}
		pos += size
	}
}

func (l *lexer) lex() []token {
	var tokens []token
	for l.pos < len(l.source) {
		if isSpace(l.source[l.pos]) {
			if l.source[l.pos] == '\n' || l.source[l.pos] == '\r' {
				l.issue("unsupported-record-separator", Unsupported, Span{l.pos, l.pos + 1}, "record separators are outside the single-record shell subset")
			}
			l.pos++
			continue
		}
		start := l.pos
		switch {
		case commentStart(l.source, l.pos):
			l.issue("unsupported-comment", Unsupported, Span{start, len(l.source)}, "comments are outside the supported shell subset")
			l.pos = len(l.source)
		case strings.HasPrefix(l.source[l.pos:], "&&"):
			l.pos += 2
			tokens = append(tokens, token{kind: tokenList, listKind: AndIf, span: Span{start, l.pos}})
		case strings.HasPrefix(l.source[l.pos:], "||"):
			l.pos += 2
			tokens = append(tokens, token{kind: tokenList, listKind: OrIf, span: Span{start, l.pos}})
		case l.source[l.pos] == ';':
			l.pos++
			tokens = append(tokens, token{kind: tokenList, listKind: Sequence, span: Span{start, l.pos}})
		case l.source[l.pos] == '|':
			l.pos++
			tokens = append(tokens, token{kind: tokenPipe, span: Span{start, l.pos}})
		case l.source[l.pos] == '&':
			l.pos++
			l.issue("unsupported-background", Unsupported, Span{start, l.pos}, "background execution is outside the supported shell subset")
		case isProcessSubstitution(l.source, l.pos):
			w := l.dynamicConstruct("unsupported-process-substitution", "process substitution is outside the supported shell subset", 2, ')')
			tokens = append(tokens, token{kind: tokenWord, word: w, span: w.Span})
		case redirectAtBoundary(l.source, l.pos):
			tokens = append(tokens, l.lexRedirect())
		default:
			w := l.lexWord()
			if w.Span.End > w.Span.Start {
				tokens = append(tokens, token{kind: tokenWord, word: w, span: w.Span})
			}
		}
	}
	return tokens
}

func (l *lexer) lexRedirect() token {
	start := l.pos
	fdStart := l.pos
	for l.pos < len(l.source) && isDigit(l.source[l.pos]) {
		l.pos++
	}
	fdSet := l.pos > fdStart
	fd := 0
	if fdSet {
		var err error
		fd, err = strconv.Atoi(l.source[fdStart:l.pos])
		if err != nil {
			l.issue("malformed-fd-redirect", Malformed, Span{fdStart, l.pos}, "file-descriptor number is out of range")
		}
	}

	opStart := l.pos
	if strings.HasPrefix(l.source[l.pos:], "<<<") {
		l.pos += 3
		l.issue("unsupported-here-string", Unsupported, Span{opStart, l.pos}, "here-strings are outside the supported shell subset")
	} else if strings.HasPrefix(l.source[l.pos:], "<<") {
		l.pos += 2
		l.issue("unsupported-here-document", Unsupported, Span{opStart, l.pos}, "here-documents are outside the supported shell subset")
	} else if strings.HasPrefix(l.source[l.pos:], ">>") {
		l.pos += 2
	} else {
		l.pos++
	}

	opText := l.source[opStart:l.pos]
	op := RedirectOperator(opText)
	if opText == "<<" || opText == "<<<" {
		op = RedirectInput
	}
	r := Redirect{FD: fd, FDSet: fdSet, Operator: op, Span: Span{start, l.pos}}
	if l.pos < len(l.source) && l.source[l.pos] == '&' && (op == RedirectInput || op == RedirectOutput) {
		l.pos++
		dupStart := l.pos
		if l.pos < len(l.source) && l.source[l.pos] == '-' {
			operandStart := l.pos
			l.pos++
			r.CloseFD = true
			if l.pos < len(l.source) && !isSpace(l.source[l.pos]) && !wordDelimiter(l.source, l.pos) {
				for l.pos < len(l.source) && !isSpace(l.source[l.pos]) && !wordDelimiter(l.source, l.pos) {
					l.pos++
				}
				l.issue("malformed-fd-redirect", Malformed, Span{operandStart, l.pos}, "file-descriptor close operand contains trailing characters")
			}
			r.Span.End = l.pos
		} else {
			for l.pos < len(l.source) && isDigit(l.source[l.pos]) {
				l.pos++
			}
			if l.pos == dupStart {
				l.issue("malformed-fd-redirect", Malformed, Span{start, l.pos}, "file-descriptor redirect requires a descriptor number or '-'")
			} else {
				var err error
				r.DuplicateFD, err = strconv.Atoi(l.source[dupStart:l.pos])
				if err != nil {
					l.issue("malformed-fd-redirect", Malformed, Span{dupStart, l.pos}, "file-descriptor number is out of range")
				} else {
					r.Duplicate = true
				}
				if l.pos < len(l.source) && !isSpace(l.source[l.pos]) && !wordDelimiter(l.source, l.pos) {
					for l.pos < len(l.source) && !isSpace(l.source[l.pos]) && !wordDelimiter(l.source, l.pos) {
						l.pos++
					}
					l.issue("malformed-fd-redirect", Malformed, Span{dupStart, l.pos}, "file-descriptor operand contains trailing characters")
				}
			}
			r.Span.End = l.pos
		}
	}
	return token{kind: tokenRedirect, redirect: r, span: r.Span}
}

func (l *lexer) lexWord() Word {
	start := l.pos
	var value strings.Builder
	var parts []WordPart
	quoted, dynamic := false, false

	for l.pos < len(l.source) && !isSpace(l.source[l.pos]) && !wordDelimiter(l.source, l.pos) {
		partStart := l.pos
		switch l.source[l.pos] {
		case '\\':
			l.pos++
			if l.pos >= len(l.source) {
				l.issue("trailing-escape", Malformed, Span{partStart, l.pos}, "word ends with an incomplete backslash escape")
				continue
			}
			r, size := utf8.DecodeRuneInString(l.source[l.pos:])
			value.WriteRune(r)
			l.pos += size
			parts = append(parts, WordPart{Kind: EscapedPart, Value: string(r), Span: Span{partStart, l.pos}})
		case '\'':
			quoted = true
			l.pos++
			contentStart := l.pos
			for l.pos < len(l.source) && l.source[l.pos] != '\'' {
				r, size := utf8.DecodeRuneInString(l.source[l.pos:])
				value.WriteRune(r)
				l.pos += size
			}
			if l.pos >= len(l.source) {
				l.issue("unclosed-single-quote", Malformed, Span{partStart, l.pos}, "single-quoted word is not closed")
				parts = append(parts, WordPart{Kind: SingleQuoted, Value: l.source[contentStart:l.pos], Span: Span{partStart, l.pos}})
				continue
			}
			l.pos++
			parts = append(parts, WordPart{Kind: SingleQuoted, Value: l.source[contentStart : l.pos-1], Span: Span{partStart, l.pos}})
		case '"':
			quoted = true
			l.pos++
			var partValue strings.Builder
			for l.pos < len(l.source) && l.source[l.pos] != '"' {
				if l.source[l.pos] == '\\' {
					escapeStart := l.pos
					l.pos++
					if l.pos >= len(l.source) {
						l.issue("trailing-escape", Malformed, Span{escapeStart, l.pos}, "double-quoted word ends with an incomplete backslash escape")
						break
					}
					r, size := utf8.DecodeRuneInString(l.source[l.pos:])
					if !strings.ContainsRune("\\\"$`", r) {
						l.issue("unsupported-double-quote-escape", Unsupported, Span{escapeStart, l.pos + size}, "double-quoted backslash escape is not common to the supported shells")
					}
					value.WriteRune(r)
					partValue.WriteRune(r)
					l.pos += size
					continue
				}
				if l.source[l.pos] == '$' || l.source[l.pos] == '`' {
					dynamic = true
					text, span := l.lexDynamic()
					value.WriteString(text)
					partValue.WriteString(text)
					parts = append(parts, WordPart{Kind: DynamicPart, Value: text, Span: span})
					continue
				}
				r, size := utf8.DecodeRuneInString(l.source[l.pos:])
				value.WriteRune(r)
				partValue.WriteRune(r)
				l.pos += size
			}
			if l.pos >= len(l.source) {
				l.issue("unclosed-double-quote", Malformed, Span{partStart, l.pos}, "double-quoted word is not closed")
			} else {
				l.pos++
			}
			parts = append(parts, WordPart{Kind: DoubleQuoted, Value: partValue.String(), Span: Span{partStart, l.pos}})
		case '$', '`':
			dynamic = true
			text, span := l.lexDynamic()
			value.WriteString(text)
			parts = append(parts, WordPart{Kind: DynamicPart, Value: text, Span: span})
		case '(', ')':
			l.pos++
			text := l.source[partStart:l.pos]
			value.WriteString(text)
			parts = append(parts, WordPart{Kind: UnquotedPart, Value: text, Span: Span{partStart, l.pos}})
			l.issue("unsupported-grouping", Unsupported, Span{partStart, l.pos}, "shell grouping is outside the supported shell subset")
		case '{', '}':
			l.pos++
			text := l.source[partStart:l.pos]
			value.WriteString(text)
			parts = append(parts, WordPart{Kind: UnquotedPart, Value: text, Span: Span{partStart, l.pos}})
			standalone := partStart == start && (l.pos == len(l.source) || isSpace(l.source[l.pos]) || wordDelimiter(l.source, l.pos))
			if !standalone {
				l.issue("unsupported-grouping", Unsupported, Span{partStart, l.pos}, "attached brace syntax is outside the supported shell subset")
			}
		default:
			for l.pos < len(l.source) && !isSpace(l.source[l.pos]) && !wordDelimiter(l.source, l.pos) && !strings.ContainsRune("\\'\"$`(){}", rune(l.source[l.pos])) {
				_, size := utf8.DecodeRuneInString(l.source[l.pos:])
				l.pos += size
			}
			text := l.source[partStart:l.pos]
			value.WriteString(text)
			parts = append(parts, WordPart{Kind: UnquotedPart, Value: text, Span: Span{partStart, l.pos}})
		}
	}
	return Word{Value: value.String(), Span: Span{start, l.pos}, Parts: parts, Quoted: quoted, Dynamic: dynamic}
}

func (l *lexer) lexDynamic() (string, Span) {
	start := l.pos
	if l.source[l.pos] == '`' {
		l.pos++
		for l.pos < len(l.source) && l.source[l.pos] != '`' {
			l.pos++
		}
		if l.pos < len(l.source) {
			l.pos++
		}
		l.issue("unsupported-command-substitution", Unsupported, Span{start, l.pos}, "command substitution is outside the supported shell subset")
		return l.source[start:l.pos], Span{start, l.pos}
	}
	if strings.HasPrefix(l.source[l.pos:], "$(") {
		returnWord := l.dynamicConstruct("unsupported-command-substitution", "command substitution is outside the supported shell subset", 2, ')')
		return l.source[returnWord.Span.Start:returnWord.Span.End], returnWord.Span
	}
	l.pos++
	if l.pos < len(l.source) && l.source[l.pos] == '{' {
		l.pos++
		for l.pos < len(l.source) && l.source[l.pos] != '}' {
			l.pos++
		}
		if l.pos < len(l.source) {
			l.pos++
		}
	} else {
		for l.pos < len(l.source) && (isNameChar(l.source[l.pos]) || isDigit(l.source[l.pos])) {
			l.pos++
		}
	}
	l.issue("unsupported-parameter-expansion", Unsupported, Span{start, l.pos}, "parameter expansion is outside the supported shell subset")
	return l.source[start:l.pos], Span{start, l.pos}
}

func (l *lexer) dynamicConstruct(code, message string, prefix int, close byte) Word {
	start := l.pos
	l.pos += prefix
	depth := 1
	for l.pos < len(l.source) && depth > 0 {
		switch l.source[l.pos] {
		case '(':
			depth++
		case close:
			depth--
		}
		l.pos++
	}
	l.issue(code, Unsupported, Span{start, l.pos}, message)
	text := l.source[start:l.pos]
	return Word{Value: text, Span: Span{start, l.pos}, Parts: []WordPart{{Kind: DynamicPart, Value: text, Span: Span{start, l.pos}}}, Dynamic: true}
}

func (l *lexer) issue(code string, kind IssueKind, span Span, message string) {
	l.issues = append(l.issues, Issue{Code: code, Kind: kind, Span: span, Message: message})
}

func parseTokens(tokens []token, issues *[]Issue) List {
	var list List
	var pipeline Pipeline
	var command SimpleCommand
	lastWasOperator := false

	finishCommand := func(at Span) bool {
		if len(command.Words) == 0 && len(command.Redirects) == 0 {
			return false
		}
		classifyAssignments(&command)
		command.Span = commandSpan(command)
		pipeline.Commands = append(pipeline.Commands, command)
		command = SimpleCommand{}
		_ = at
		return true
	}
	finishPipeline := func() bool {
		if len(pipeline.Commands) == 0 {
			return false
		}
		pipeline.Span = Span{Start: pipeline.Commands[0].Span.Start, End: pipeline.Commands[len(pipeline.Commands)-1].Span.End}
		list.Pipelines = append(list.Pipelines, pipeline)
		pipeline = Pipeline{}
		return true
	}

	for _, tok := range tokens {
		switch tok.kind {
		case tokenWord:
			command.Words = append(command.Words, tok.word)
			lastWasOperator = false
		case tokenRedirect:
			r := tok.redirect
			if !r.Duplicate && !r.CloseFD {
				// A redirect target may be adjacent or whitespace-separated, so it
				// is attached when the following word token arrives.
				command.Redirects = append(command.Redirects, r)
			} else {
				command.Redirects = append(command.Redirects, r)
			}
			lastWasOperator = false
		case tokenPipe:
			attachRedirectTargets(&command, issues)
			if !finishCommand(tok.span) {
				*issues = append(*issues, Issue{Code: "empty-pipeline-command", Kind: Malformed, Span: tok.span, Message: "pipeline contains an empty command"})
			}
			pipeline.Pipes = append(pipeline.Pipes, tok.span)
			lastWasOperator = true
		case tokenList:
			attachRedirectTargets(&command, issues)
			if !finishCommand(tok.span) {
				*issues = append(*issues, Issue{Code: "empty-list-segment", Kind: Malformed, Span: tok.span, Message: "list operator has no command on its left"})
			}
			finishPipeline()
			list.Operators = append(list.Operators, ListOperator{Kind: tok.listKind, Span: tok.span})
			lastWasOperator = true
		}
		if tok.kind == tokenWord {
			attachLatestRedirectTarget(&command)
		}
	}
	attachRedirectTargets(&command, issues)
	finishCommand(Span{Start: len(list.Pipelines), End: len(list.Pipelines)})
	finishPipeline()
	if lastWasOperator && len(tokens) > 0 {
		span := tokens[len(tokens)-1].span
		*issues = append(*issues, Issue{Code: "trailing-operator", Kind: Malformed, Span: span, Message: "command ends with an incomplete shell operator"})
	}
	if len(tokens) == 0 {
		*issues = append(*issues, Issue{Code: "empty-command", Kind: Malformed, Span: Span{0, 0}, Message: "command is empty"})
	}
	return list
}

func attachLatestRedirectTarget(command *SimpleCommand) {
	if len(command.Redirects) == 0 || len(command.Words) == 0 {
		return
	}
	r := &command.Redirects[len(command.Redirects)-1]
	if r.Target != nil || r.Duplicate || r.CloseFD || r.Span.End > command.Words[len(command.Words)-1].Span.Start {
		return
	}
	w := command.Words[len(command.Words)-1]
	r.Target = &w
	r.Span.End = w.Span.End
	command.Words = command.Words[:len(command.Words)-1]
}

func attachRedirectTargets(command *SimpleCommand, issues *[]Issue) {
	for i := range command.Redirects {
		r := &command.Redirects[i]
		if r.Target == nil && !r.Duplicate && !r.CloseFD {
			*issues = append(*issues, Issue{Code: "missing-redirect-target", Kind: Malformed, Span: r.Span, Message: "redirect is missing its target"})
		}
	}
}

func classifyAssignments(command *SimpleCommand) {
	for _, word := range command.Words {
		name, ok := assignmentName(word)
		if !ok {
			break
		}
		command.Assignments = append(command.Assignments, Assignment{Name: name, Word: word, Span: word.Span})
	}
}

func assignmentName(word Word) (string, bool) {
	if word.Dynamic || len(word.Parts) == 0 {
		return "", false
	}
	raw := word.Parts[0]
	if raw.Kind != UnquotedPart {
		return "", false
	}
	eq := strings.IndexByte(raw.Value, '=')
	if eq < 1 {
		return "", false
	}
	name := raw.Value[:eq]
	if !isNameStart(name[0]) {
		return "", false
	}
	for i := 1; i < len(name); i++ {
		if !isNameChar(name[i]) && !isDigit(name[i]) {
			return "", false
		}
	}
	return name, true
}

func commandSpan(command SimpleCommand) Span {
	start, end := -1, -1
	for _, w := range command.Words {
		start, end = growSpan(start, end, w.Span)
	}
	for _, r := range command.Redirects {
		start, end = growSpan(start, end, r.Span)
	}
	return Span{Start: start, End: end}
}

func growSpan(start, end int, span Span) (int, int) {
	if start < 0 || span.Start < start {
		start = span.Start
	}
	if span.End > end {
		end = span.End
	}
	return start, end
}

func appendStructuralIssues(result *Result) {
	for pi := range result.List.Pipelines {
		for ci := range result.List.Pipelines[pi].Commands {
			command := &result.List.Pipelines[pi].Commands[ci]
			if word := commandControlWord(*command); word != nil {
				result.Issues = append(result.Issues, Issue{
					Code: "unsupported-control-syntax", Kind: Unsupported, Span: word.Span,
					Message: "shell reserved word or control syntax is outside the supported subset",
				})
			}
			resolution := ResolveExecutable(*command)
			result.Issues = append(result.Issues, resolution.Issues...)
		}
	}
}

func commandControlWord(command SimpleCommand) *Word {
	index := len(command.Assignments)
	if index >= len(command.Words) {
		return nil
	}
	word := &command.Words[index]
	if !plainUnquotedWord(*word) {
		return nil
	}
	switch word.Value {
	case "!", "if", "then", "else", "elif", "fi", "for", "while", "until", "do", "done", "case", "esac", "function", "select", "in", "time", "coproc", "repeat", "foreach", "end", "always", "nocorrect", "noglob", "begin", "switch", "and", "or", "not", "break", "continue", "return", "builtin", "exec", "declare", "export", "float", "integer", "local", "readonly", "typeset", "{", "}", "[[", "]]":
		return word
	default:
		return nil
	}
}

func plainUnquotedWord(word Word) bool {
	return !word.Quoted && !word.Dynamic && len(word.Parts) == 1 && word.Parts[0].Kind == UnquotedPart
}

func isSpace(b byte) bool     { return b == ' ' || b == '\t' || b == '\n' || b == '\r' }
func isDigit(b byte) bool     { return b >= '0' && b <= '9' }
func isNameStart(b byte) bool { return b == '_' || b >= 'A' && b <= 'Z' || b >= 'a' && b <= 'z' }
func isNameChar(b byte) bool  { return isNameStart(b) }

func wordDelimiter(source string, pos int) bool {
	b := source[pos]
	return b == ';' || b == '|' || b == '&' || b == '<' || b == '>' || isProcessSubstitution(source, pos)
}

func redirectAtBoundary(source string, pos int) bool {
	if pos >= len(source) {
		return false
	}
	if source[pos] == '<' || source[pos] == '>' {
		return true
	}
	if !isDigit(source[pos]) {
		return false
	}
	for pos < len(source) && isDigit(source[pos]) {
		pos++
	}
	return pos < len(source) && (source[pos] == '<' || source[pos] == '>')
}

func isProcessSubstitution(source string, pos int) bool {
	return pos+1 < len(source) && (source[pos] == '<' || source[pos] == '>') && source[pos+1] == '('
}

func commentStart(source string, pos int) bool {
	if source[pos] != '#' {
		return false
	}
	if pos == 0 {
		return true
	}
	previous := source[pos-1]
	return isSpace(previous) || previous == ';' || previous == '|' || previous == '&'
}
