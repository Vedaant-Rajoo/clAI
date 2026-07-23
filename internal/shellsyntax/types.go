// Package shellsyntax parses a deliberately small, non-evaluating shell syntax
// subset shared by Fish, Bash, and Zsh.
package shellsyntax

// Span is a half-open byte range in the original source.
type Span struct {
	Start int
	End   int
}

// IssueKind distinguishes syntax errors from well-formed syntax outside the
// supported common subset.
type IssueKind string

const (
	Malformed   IssueKind = "malformed"
	Unsupported IssueKind = "unsupported"
)

// Issue records parser uncertainty without interpreting or evaluating it.
type Issue struct {
	Code    string
	Kind    IssueKind
	Span    Span
	Message string
}

// Result is the complete parse result. Callers must inspect Issues before
// treating the parsed structure as eligible for policy evaluation.
type Result struct {
	Source string
	List   List
	Issues []Issue
}

// Supported reports whether the entire input belongs to the documented subset.
func (r Result) Supported() bool { return len(r.Issues) == 0 }

// List is a sequence of pipelines joined by list operators.
type List struct {
	Pipelines []Pipeline
	Operators []ListOperator
	Span      Span
}

// ListOperator joins adjacent pipelines.
type ListOperator struct {
	Kind ListOperatorKind
	Span Span
}

type ListOperatorKind string

const (
	Sequence ListOperatorKind = ";"
	AndIf    ListOperatorKind = "&&"
	OrIf     ListOperatorKind = "||"
)

// Pipeline is one or more simple commands joined by pipes.
type Pipeline struct {
	Commands []SimpleCommand
	Pipes    []Span
	Span     Span
}

// SimpleCommand retains words and redirects separately while preserving source
// order through each element's span.
type SimpleCommand struct {
	Words       []Word
	Assignments []Assignment
	Redirects   []Redirect
	Span        Span
}

// Assignment is a leading NAME=value word.
type Assignment struct {
	Name string
	Word Word
	Span Span
}

// Word is a shell word after quote removal and backslash decoding. Source is
// always recoverable through Span and Result.Source.
type Word struct {
	Value   string
	Span    Span
	Parts   []WordPart
	Quoted  bool
	Dynamic bool
}

type WordPart struct {
	Kind  WordPartKind
	Value string
	Span  Span
}

type WordPartKind string

const (
	UnquotedPart WordPartKind = "unquoted"
	EscapedPart  WordPartKind = "escaped"
	SingleQuoted WordPartKind = "single-quoted"
	DoubleQuoted WordPartKind = "double-quoted"
	DynamicPart  WordPartKind = "dynamic"
)

type Redirect struct {
	FD          int
	FDSet       bool
	Operator    RedirectOperator
	Target      *Word
	DuplicateFD int
	Duplicate   bool
	CloseFD     bool
	Span        Span
}

type RedirectOperator string

const (
	RedirectInput  RedirectOperator = "<"
	RedirectOutput RedirectOperator = ">"
	RedirectAppend RedirectOperator = ">>"
)
