package shellsyntax

import (
	"reflect"
	"testing"
)

// TestEmptyBracePairIsWordData proves a literal {} pair parses as ordinary word
// data outside command position. Fish, Bash, and Zsh all render {} literally in
// every word position, so idioms such as `xargs -I{}` and `find -exec {} \;`
// belong inside the supported subset.
func TestEmptyBracePairIsWordData(t *testing.T) {
	supported := []string{
		`xargs -I{} du -h {}`,
		`find . -exec du -h {} \;`,
		`find . -exec du -h {} +`,
		`echo -I{}x`,
		`echo a{}b`,
		`echo {}x`,
		`FOO={} echo ok`,
		`find . -name '*.log' | xargs -I{} du -h {}`,
	}
	for _, source := range supported {
		t.Run(source, func(t *testing.T) {
			result := Parse(source)
			if !result.Supported() {
				t.Fatalf("Parse(%q) issues = %#v; a literal brace pair outside command position is data", source, result.Issues)
			}
		})
	}
}

// TestEmptyBracePairLexesAsOneUnquotedPart pins the part shape the dispatch
// guard relies on: the pair is a single unquoted part with the exact value {},
// so a brace can never appear inside a longer unquoted run.
func TestEmptyBracePairLexesAsOneUnquotedPart(t *testing.T) {
	result := Parse(`xargs -I{} {}`)
	if !result.Supported() {
		t.Fatalf("issues = %#v", result.Issues)
	}
	words := result.List.Pipelines[0].Commands[0].Words
	if len(words) != 3 {
		t.Fatalf("words = %#v, want three", words)
	}

	flag := words[1]
	if flag.Value != "-I{}" {
		t.Fatalf("flag value = %q, want -I{}", flag.Value)
	}
	gotKinds := make([]string, 0, len(flag.Parts))
	gotValues := make([]string, 0, len(flag.Parts))
	for _, part := range flag.Parts {
		if part.Kind != UnquotedPart {
			t.Fatalf("part %#v is not unquoted", part)
		}
		gotKinds = append(gotKinds, "unquoted")
		gotValues = append(gotValues, part.Value)
	}
	if !reflect.DeepEqual(gotValues, []string{"-I", "{}"}) {
		t.Fatalf("flag parts = %v, want [-I {}]", gotValues)
	}

	bare := words[2]
	if len(bare.Parts) != 1 || bare.Parts[0].Value != "{}" || bare.Parts[0].Span != (Span{11, 13}) {
		t.Fatalf("bare brace parts = %#v, want one {} part spanning 11-13", bare.Parts)
	}
}

// TestBraceDispatchPositionRejected proves the pair stays rejected wherever it
// can name an executable. executableBase reduces {}/echo to echo, so without
// this guard a brace-bearing word could reach the read-only allow list.
func TestBraceDispatchPositionRejected(t *testing.T) {
	tests := []struct {
		name   string
		source string
		span   Span
	}{
		{name: "bare pair", source: `{}`, span: Span{0, 2}},
		{name: "pair as path prefix", source: `{}/echo`, span: Span{0, 7}},
		{name: "pair before destructive base", source: `{}/rm`, span: Span{0, 5}},
		// Wrapper unwrapping means the real executable sits deeper than the
		// command's first word: checking Words[0] alone would miss this.
		{name: "behind env wrapper", source: `env {}/echo x`, span: Span{4, 11}},
		{name: "behind command wrapper", source: `command {}/echo`, span: Span{8, 15}},
		// The braced word is itself consumed as a wrapper, so the resolved
		// executable is the innocent trailing word: checking only the resolved
		// word would miss this.
		{name: "pair is the wrapper", source: `{}/env echo x`, span: Span{0, 6}},
		{name: "pair with assignments", source: `FOO=bar {}/echo`, span: Span{8, 15}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := Parse(test.source)
			if result.Supported() {
				t.Fatalf("Parse(%q) reported Supported; a brace pair in command position is not data", test.source)
			}
			issue := issueByCode(result, "unsupported-brace-dispatch")
			if issue == nil {
				t.Fatalf("issues = %#v, want unsupported-brace-dispatch", result.Issues)
			}
			if issue.Span != test.span {
				t.Fatalf("span = %#v, want %#v covering the whole dispatch word", issue.Span, test.span)
			}
			if issue.Kind != Unsupported {
				t.Fatalf("kind = %v, want Unsupported", issue.Kind)
			}
		})
	}
}

// TestBraceExpansionAndGroupingStayUnsupported proves the narrowing is confined
// to the empty pair. Brace expansion alters word count and is dialect-divergent
// (`{1..3}` expands in Bash and Zsh but not Fish), and brace groups alter
// execution structure, so both remain outside the subset.
func TestBraceExpansionAndGroupingStayUnsupported(t *testing.T) {
	tests := []struct {
		source string
		code   string
	}{
		{source: `echo {a,b}`, code: "unsupported-grouping"},
		{source: `echo {1..3}`, code: "unsupported-grouping"},
		{source: `echo {a}`, code: "unsupported-grouping"},
		{source: `{ echo hi; }`, code: "unsupported-control-syntax"},
		{source: `{ rm -rf /; }`, code: "unsupported-control-syntax"},
		{source: `{cmd`, code: "unsupported-grouping"},
		{source: `echo value}`, code: "unsupported-grouping"},
		{source: `{}}`, code: "unsupported-grouping"},
		{source: `{{}}`, code: "unsupported-grouping"},
	}
	for _, test := range tests {
		t.Run(test.source, func(t *testing.T) {
			result := Parse(test.source)
			if result.Supported() {
				t.Fatalf("Parse(%q) reported Supported", test.source)
			}
			if !hasIssue(result, test.code) {
				t.Fatalf("issues = %#v, want %s", result.Issues, test.code)
			}
		})
	}
}

// TestQuotedBraceRemainsData is a no-disturbance witness: quoted and escaped
// braces were already data and must be untouched by the lexer change.
func TestQuotedBraceRemainsData(t *testing.T) {
	for _, source := range []string{`echo "{}"`, `echo '{}'`, `echo \{\}`, `awk '{print $1}'`, `echo '{'`, `echo \}`} {
		if result := Parse(source); !result.Supported() {
			t.Fatalf("Parse(%q) issues = %#v; quoted and escaped braces are data", source, result.Issues)
		}
	}
	// A quoted pair is not an unquoted part, so it never satisfies the dispatch
	// guard even in command position.
	if result := Parse(`"{}"`); !result.Supported() {
		t.Fatalf(`Parse("{}") issues = %#v`, result.Issues)
	}
}

// TestBraceParseDeterminism keeps the new paths reproducible, matching the
// determinism guarantee the wider issue table asserts.
func TestBraceParseDeterminism(t *testing.T) {
	for _, source := range []string{`xargs -I{} du -h {}`, `{}/env echo x`, `{{}}`} {
		first := Parse(source)
		second := Parse(source)
		if !reflect.DeepEqual(first.Issues, second.Issues) {
			t.Fatalf("Parse(%q) issues differ across runs: %#v then %#v", source, first.Issues, second.Issues)
		}
		for _, issue := range first.Issues {
			if issue.Span.Start < 0 || issue.Span.End < issue.Span.Start || issue.Span.End > len(source) {
				t.Fatalf("issue %#v has an invalid span for %q", issue, source)
			}
		}
	}
}
