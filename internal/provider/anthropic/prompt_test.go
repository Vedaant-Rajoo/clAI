package anthropic

import (
	"strings"
	"testing"

	"github.com/Vedaant-Rajoo/clai/internal/shellsyntax"
)

// The system prompt tells the model which shell syntax survives review. An
// inaccurate prompt is worse than none: it either steers the model away from
// commands clai would accept, or toward commands clai rejects after the request
// has already been paid for. These tests hold the prompt and the parser to the
// same truth in both directions.
//
// OpenRouter's copy of the same guidance is pinned byte-for-byte by its golden
// request-body test; the direct provider has no such golden because its system
// block is not part of the user-message payload, so the claims are checked here
// against the parser instead.

func TestSystemPromptAllowedSyntaxParses(t *testing.T) {
	allowed := []string{
		`ls -la`,
		`echo 'single quoted'`,
		`echo "double quoted"`,
		`echo escaped\ word`,
		`FOO=bar echo ok`,
		`FOO=bar BAZ=qux echo ok`,
		`env FOO=bar echo ok`,
		`env -i echo ok`,
		`env -- echo ok`,
		`command echo ok`,
		`command -- echo ok`,
		`echo one; echo two`,
		`echo one && echo two`,
		`echo one || echo two`,
		`echo one | grep one`,
		`echo ok > out.txt`,
		`echo ok >> out.txt`,
		`sort < in.txt`,
		`xargs -I{} du -h {}`,
		`find . -exec du -h {} \;`,
	}
	for _, command := range allowed {
		t.Run(command, func(t *testing.T) {
			if result := shellsyntax.Parse(command); !result.Supported() {
				t.Fatalf("the system prompt describes %q as usable, but the parser rejects it: %#v", command, result.Issues)
			}
		})
	}
}

func TestSystemPromptForbiddenSyntaxIsRejected(t *testing.T) {
	forbidden := []string{
		`echo $(date)`,
		"echo `date`",
		`echo $HOME`,
		`echo ${HOME}`,
		`echo {a,b}`,
		`echo {1..3}`,
		`{ echo hi; }`,
		`( echo hi )`,
		`echo ok # comment`,
		`sleep 5 &`,
		`sh -c 'echo ok'`,
		`bash -c 'echo ok'`,
		`cat <<EOF`,
		`cat <<<'text'`,
	}
	for _, command := range forbidden {
		t.Run(command, func(t *testing.T) {
			if result := shellsyntax.Parse(command); result.Supported() {
				t.Fatalf("the system prompt tells the model never to use %q, but the parser accepts it", command)
			}
		})
	}
}

// TestSystemPromptStatesTheSyntaxRules keeps the prompt carrying the claims the
// two corpora above verify. Without it the guidance could be dropped from the
// prompt while the corpus tests kept passing against the parser alone.
func TestSystemPromptStatesTheSyntaxRules(t *testing.T) {
	claims := []string{
		"shell syntax subset shared by Fish, Bash, and Zsh",
		"leading NAME=value assignments",
		"env and command wrappers",
		"literal brace pair {}",
		"xargs -I{}",
		`find -exec {} \;`,
		"command substitution $(...)",
		"parameter expansion",
		"brace expansion",
		"brace groups",
		"subshells",
		"here-documents",
		"background &",
	}
	for _, claim := range claims {
		if !strings.Contains(systemPrompt, claim) {
			t.Errorf("system prompt no longer states %q; update the syntax corpora in this file if the contract changed", claim)
		}
	}
	// The prompt must never carry credential or endpoint material.
	for _, forbidden := range []string{"api-key", "x-api-key", "Authorization", "sk-ant", defaultEndpoint} {
		if strings.Contains(systemPrompt, forbidden) {
			t.Errorf("system prompt contains %q", forbidden)
		}
	}
}
