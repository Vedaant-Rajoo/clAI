package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunRejectsPositionalArgumentsBeforeLoadingFiles(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	if code := run([]string{"unexpected"}, &stdout, &stderr); code != 2 {
		t.Fatalf("run() code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "unexpected positional arguments") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestRunRejectsMutuallyExclusiveExecutionFlags(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	code := run([]string{"--run-case", "AC-X-001", "--run-all-automated"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("run() code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "mutually exclusive") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}
