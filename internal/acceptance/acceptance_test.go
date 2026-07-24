package acceptance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

func TestRequirementMarkers(t *testing.T) {
	t.Parallel()

	valid := []byte("<!-- note: ordinary prose comment -->\nNormative text. <!-- requirement: REQ-ONE-001 -->\n")
	ids, err := RequirementIDs(valid)
	if err != nil || len(ids) != 1 || ids[0] != "REQ-ONE-001" {
		t.Fatalf("RequirementIDs(valid) = %v, %v", ids, err)
	}

	tests := []struct {
		name string
		spec string
		want string
	}{
		{"duplicate ID", "One. <!-- requirement: REQ-DUP-001 -->\nTwo. <!-- requirement: REQ-DUP-001 -->\n", "duplicate requirement ID"},
		{"lowercase ID", "One. <!-- requirement: req-one-001 -->\n", "malformed requirement marker"},
		{"uppercase keyword", "One. <!-- Requirement: REQ-ONE-001 -->\n", "malformed requirement marker"},
		{"partial keyword", "One. <!-- require: REQ-ONE-001 -->\n", "malformed requirement marker"},
		{"short req label", "One. <!-- req: REQ-BYPASS-001 -->\n", "malformed requirement marker"},
		{"mixed case short label", "One. <!-- rEq: REQ-BYPASS-001 -->\n", "malformed requirement marker"},
		{"consonant abbreviation", "One. <!-- rqmt: REQ-BYPASS-001 -->\n", "malformed requirement marker"},
		{"plural label", "One. <!-- requirements: REQ-BYPASS-001 -->\n", "malformed requirement marker"},
		{"unrelated label carrying requirement ID", "One. <!-- tracking: REQ-BYPASS-001 -->\n", "malformed requirement marker"},
		{"abbreviated label without ID", "One. <!-- req: pending -->\n", "malformed requirement marker"},
		{"missing close", "One. <!-- requirement: REQ-ONE-001\n", "malformed requirement marker"},
		{"detached", "<!-- requirement: REQ-ONE-001 -->\n", "malformed requirement marker"},
		{"heading only", "## <!-- requirement: REQ-ONE-001 -->\n", "detached from substantive"},
		{"multiple comments", "One. <!-- requirement: REQ-ONE-001 --> <!-- note -->\n", "malformed requirement marker"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := RequirementIDs([]byte(test.spec))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("RequirementIDs() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestDecodeRejectsRepeatedJSONKeysAtEveryObjectLevel(t *testing.T) {
	t.Parallel()

	tests := []string{
		`{"schema":"a","schema":"b"}`,
		`{"outer":{"key":1,"key":2}}`,
		`{"array":[{"key":1,"key":2}]}`,
		`{"outer":{"array":[{"deep":1,"deep":2}]}}`,
	}
	for _, data := range tests {
		data := data
		t.Run(data, func(t *testing.T) {
			t.Parallel()
			if err := rejectDuplicateJSONKeys([]byte(data)); err == nil || !strings.Contains(err.Error(), "duplicate JSON object key") {
				t.Fatalf("rejectDuplicateJSONKeys() error = %v", err)
			}
		})
	}
}

func TestValidateRejectsInvalidMappingsAndRevisionLoss(t *testing.T) {
	t.Parallel()

	predecessor := []byte("one <!-- requirement: REQ-ONE-001 -->\ntwo <!-- requirement: REQ-TWO-001 -->\n")
	current := []byte("one <!-- requirement: REQ-ONE-001 -->\n")
	previousIDs, err := RequirementIDs(predecessor)
	if err != nil {
		t.Fatal(err)
	}
	currentIDs, err := RequirementIDs(current)
	if err != nil {
		t.Fatal(err)
	}
	base := validManifest(current, predecessor, currentIDs)
	base.Requirements = append(base.Requirements, Requirement{ID: "REQ-TWO-001", Summary: "two", Status: "retired"})

	tests := []struct {
		name string
		edit func(*Manifest)
		want string
	}{
		{"duplicate manifest requirement", func(m *Manifest) { m.Requirements[1].ID = m.Requirements[0].ID }, "duplicate requirement ID"},
		{"unknown active requirement", func(m *Manifest) { m.Requirements[0].ID = "REQ-UNKNOWN-001" }, "absent from current specification"},
		{"uncovered current requirement", func(m *Manifest) { m.Cases[0].RequirementIDs = nil }, "has no proof mapping"},
		{"duplicate case", func(m *Manifest) { m.Cases = append(m.Cases, m.Cases[0]) }, "duplicate acceptance-case ID"},
		{"unknown case requirement", func(m *Manifest) { m.Cases[0].RequirementIDs[0] = "REQ-UNKNOWN-001" }, "unknown current requirement ID"},
		{"silent disappearance", func(m *Manifest) { m.Requirements = m.Requirements[:1] }, "disappeared without retired or superseded metadata"},
		{"bad supersession", func(m *Manifest) { m.Requirements[1].Status = "superseded" }, "must be absent and name a valid replacement"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			manifest := cloneManifest(base)
			test.edit(&manifest)
			err := Validate(current, predecessor, currentIDs, previousIDs, manifest)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestTimeoutValidation(t *testing.T) {
	t.Parallel()

	spec := []byte("one <!-- requirement: REQ-ONE-001 -->\n")
	ids, _ := RequirementIDs(spec)
	for _, timeout := range []int64{0, -1, MaxTimeoutSecond + 1, math.MaxInt64} {
		timeout := timeout
		t.Run(fmt.Sprint(timeout), func(t *testing.T) {
			t.Parallel()
			manifest := validManifest(spec, spec, ids)
			manifest.Cases[0].TimeoutSeconds = timeout
			err := Validate(spec, spec, ids, ids, manifest)
			if err == nil || !strings.Contains(err.Error(), "timeout_seconds") {
				t.Fatalf("Validate() error = %v", err)
			}
		})
	}
	for _, literal := range []string{"0", "-1", "1.5", "9223372036854775808"} {
		literal := literal
		t.Run("decode-"+literal, func(t *testing.T) {
			t.Parallel()
			raw := strings.Replace(validManifestJSON(), `"timeout_seconds":5`, `"timeout_seconds":`+literal, 1)
			_, err := decodeManifest([]byte(raw))
			if literal == "0" || literal == "-1" {
				if err != nil {
					return
				}
				return
			}
			if err == nil {
				t.Fatalf("decodeManifest(%s) unexpectedly succeeded", literal)
			}
		})
	}
}

func TestRunAutomatedCaseEvidenceProtocol(t *testing.T) {
	t.Parallel()

	base := Case{ID: "AC-RUNNER-TEST", ProofType: "automated", TimeoutSeconds: 5, PermittedSkipConditions: []string{"shell unavailable"}, Evidence: Evidence{Marker: "PASS", Location: "combined-output"}}
	tests := []struct {
		name    string
		command string
		want    string
	}{
		{"substring marker", "printf 'PASS\\n'", "exactly one evidence record"},
		{"partial marker record", "printf 'ACCEPTANCE-EVIDENCE nope\\n'", "malformed ACCEPTANCE-EVIDENCE"},
		{"wrong marker", commandEvidence("AC-RUNNER-TEST", "PASS-extra", "combined-output"), "evidence record mismatch"},
		{"wrong location", commandEvidence("AC-RUNNER-TEST", "PASS", "stdout"), "evidence record mismatch"},
		{"wrong case", commandEvidence("AC-OTHER", "PASS", "combined-output"), "evidence record mismatch"},
		{"multiple evidence", commandEvidence("AC-RUNNER-TEST", "PASS", "combined-output") + "; " + commandEvidence("AC-RUNNER-TEST", "PASS", "combined-output"), "exactly one evidence record"},
		{"no tests", "printf '[no tests to run]\\n'; " + commandEvidence("AC-RUNNER-TEST", "PASS", "combined-output"), "reported [no tests to run]"},
		{"unrelated skip words", "printf 'shell unavailable\\n'; " + commandEvidence("AC-RUNNER-TEST", "PASS", "combined-output"), ""},
		{"legacy skip output", "printf '%s\\n' '--- SKIP: TestThing'; " + commandEvidence("AC-RUNNER-TEST", "PASS", "combined-output"), "without an explicit ACCEPTANCE-SKIP"},
		{"substring skip reason", commandSkip("AC-RUNNER-TEST", "shell unavailable today"), "invalid or undeclared skip"},
		{"wrong skip case", commandSkip("AC-OTHER", "shell unavailable"), "invalid or undeclared skip"},
		{"skip plus evidence", commandSkip("AC-RUNNER-TEST", "shell unavailable") + "; " + commandEvidence("AC-RUNNER-TEST", "PASS", "combined-output"), "exactly one skip record"},
		{"nonzero cannot be masked", commandSkip("AC-RUNNER-TEST", "shell unavailable") + "; exit 7", "exited nonzero"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			acceptanceCase := base
			acceptanceCase.Command = test.command
			result, err := RunAutomatedCase(context.Background(), t.TempDir(), acceptanceCase)
			if test.want == "" {
				if err != nil || result.Skipped {
					t.Fatalf("result=%+v err=%v", result, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("RunAutomatedCase() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestRunAutomatedCaseAllowsExactDeclaredSkip(t *testing.T) {
	t.Parallel()
	acceptanceCase := Case{ID: "AC-RUNNER-SKIP", ProofType: "automated", Command: commandSkip("AC-RUNNER-SKIP", "shell unavailable"), TimeoutSeconds: 5, PermittedSkipConditions: []string{"shell unavailable"}, Evidence: Evidence{Marker: "PASS", Location: "combined-output"}}
	result, err := RunAutomatedCase(context.Background(), t.TempDir(), acceptanceCase)
	if err != nil || !result.Skipped || result.SkipReason != "shell unavailable" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestValidateFindingsRequireIndependentClosure(t *testing.T) {
	t.Parallel()
	spec := []byte("one <!-- requirement: REQ-ONE-001 -->\n")
	ids, _ := RequirementIDs(spec)
	manifest := validManifest(spec, spec, ids)
	manifest.Findings = []Finding{{ID: "P0-TEST-001", RequirementIDs: ids, Summary: "finding", Status: "open", RequiredRegressionCaseIDs: []string{"AC-VALID-001"}, ReviewedRevision: "old", ReplacementRevision: "new", IndependentReviewEvidence: IndependentReviewEvidence{Required: true, Location: ".local/evidence/review.json", Marker: "REVIEW-CLOSURE"}}}
	if err := Validate(spec, spec, ids, ids, manifest); err != nil {
		t.Fatal(err)
	}
	manifest.Findings[0].Status = "fixed-and-conforming"
	if err := Validate(spec, spec, ids, ids, manifest); err == nil || !strings.Contains(err.Error(), "complete independent closure") {
		t.Fatalf("Validate() error=%v", err)
	}
	manifest.Findings[0].Closure = &Closure{Status: "fixed-and-conforming", Reviewer: manifest.Producer, Revision: manifest.RevisionLabel, Evidence: ".local/evidence/review.json", RecordedAt: "2026-07-22T12:00:00Z"}
	if err := Validate(spec, spec, ids, ids, manifest); err == nil || !strings.Contains(err.Error(), "different producer") {
		t.Fatalf("self-closure Validate() error=%v", err)
	}
	manifest.Findings[0].Closure.Reviewer = "fresh-reviewer"
	if err := Validate(spec, spec, ids, ids, manifest); err != nil {
		t.Fatalf("independent closure rejected: %v", err)
	}
}

func TestValidateIndependentReviewEvidenceFile(t *testing.T) {
	baseManifest := func() Manifest {
		return Manifest{RevisionLabel: "phase-0-remediation-r2", Producer: "implementation-worker", Findings: []Finding{{
			ID: "P0-TEST-001", Summary: "finding", Status: "fixed-and-conforming",
			ReviewedRevision: "phase-0-remediation-r1", ReplacementRevision: "phase-0-remediation-r2",
			IndependentReviewEvidence: IndependentReviewEvidence{Required: true, Location: ".local/evidence/reviews/P0-TEST-001.json", Marker: "INDEPENDENT-REVIEW-CLOSURE"},
			Closure:                   &Closure{Status: "fixed-and-conforming", Reviewer: "fresh-reviewer", Revision: "phase-0-remediation-r2", Evidence: ".local/evidence/reviews/P0-TEST-001.json", RecordedAt: "2026-07-22T12:00:00Z"},
		}}}
	}
	validRecord := func() independentReviewRecord {
		return independentReviewRecord{Schema: "independent-review-evidence/v1", FindingID: "P0-TEST-001", Status: "fixed-and-conforming", Reviewer: "fresh-reviewer", ReviewedRevision: "phase-0-remediation-r1", ReplacementRevision: "phase-0-remediation-r2", Marker: "INDEPENDENT-REVIEW-CLOSURE", RecordedAt: "2026-07-22T12:00:00Z"}
	}
	setup := func(t *testing.T) (string, string) {
		t.Helper()
		root := t.TempDir()
		evidenceRoot := filepath.Join(root, ".local", "evidence")
		reviewRoot := filepath.Join(evidenceRoot, "reviews")
		if err := os.MkdirAll(reviewRoot, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(evidenceRoot, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(reviewRoot, 0o700); err != nil {
			t.Fatal(err)
		}
		return root, filepath.Join(reviewRoot, "P0-TEST-001.json")
	}
	write := func(t *testing.T, path string, value any, mode os.FileMode) {
		t.Helper()
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, mode); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("valid", func(t *testing.T) {
		root, path := setup(t)
		write(t, path, validRecord(), 0o600)
		if err := ValidateIndependentReviewEvidence(root, baseManifest()); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("absent", func(t *testing.T) {
		root, _ := setup(t)
		if err := ValidateIndependentReviewEvidence(root, baseManifest()); err == nil {
			t.Fatal("absent evidence accepted")
		}
	})
	t.Run("malformed", func(t *testing.T) {
		root, path := setup(t)
		if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := ValidateIndependentReviewEvidence(root, baseManifest()); err == nil {
			t.Fatal("malformed evidence accepted")
		}
	})
	t.Run("mismatched finding", func(t *testing.T) {
		root, path := setup(t)
		record := validRecord()
		record.FindingID = "P0-OTHER-001"
		write(t, path, record, 0o600)
		if err := ValidateIndependentReviewEvidence(root, baseManifest()); err == nil {
			t.Fatal("mismatched evidence accepted")
		}
	})
	t.Run("mismatched revisions marker and time", func(t *testing.T) {
		root, path := setup(t)
		record := validRecord()
		record.ReviewedRevision = "wrong"
		record.ReplacementRevision = "wrong"
		record.Marker = "wrong"
		record.RecordedAt = "2026-07-22T13:00:00Z"
		write(t, path, record, 0o600)
		if err := ValidateIndependentReviewEvidence(root, baseManifest()); err == nil {
			t.Fatal("mismatched evidence accepted")
		}
	})
	t.Run("self authored", func(t *testing.T) {
		root, path := setup(t)
		manifest := baseManifest()
		manifest.Findings[0].Closure.Reviewer = manifest.Producer
		record := validRecord()
		record.Reviewer = manifest.Producer
		write(t, path, record, 0o600)
		if err := ValidateIndependentReviewEvidence(root, manifest); err == nil {
			t.Fatal("self-authored evidence accepted")
		}
	})
	t.Run("non private", func(t *testing.T) {
		root, path := setup(t)
		write(t, path, validRecord(), 0o644)
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := ValidateIndependentReviewEvidence(root, baseManifest()); err == nil {
			t.Fatal("non-private evidence accepted")
		}
	})
	t.Run("symlink", func(t *testing.T) {
		root, path := setup(t)
		target := path + ".target"
		write(t, target, validRecord(), 0o600)
		if err := os.Symlink(target, path); err != nil {
			t.Fatal(err)
		}
		if err := ValidateIndependentReviewEvidence(root, baseManifest()); err == nil {
			t.Fatal("symlink evidence accepted")
		}
	})
}

func TestValidateArtifactMetadata(t *testing.T) {
	t.Parallel()
	spec := []byte("one <!-- requirement: REQ-ONE-001 -->\ntwo <!-- requirement: REQ-TWO-001 -->\n")
	ids, _ := RequirementIDs(spec)
	manifest := validManifest(spec, spec, ids)
	manifestData := []byte("manifest bytes")
	registry := artifactRegistry(manifest, sha256Hex(manifestData), sha256Hex(spec))
	if err := ValidateArtifactRegistry([]byte(registry), manifestData, spec, manifest); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"producer", "consumers", "compatibility"} {
		field := field
		t.Run(field, func(t *testing.T) {
			broken := strings.Replace(registry, "    "+field+":", "    missing_"+field+":", 1)
			if err := ValidateArtifactRegistry([]byte(broken), manifestData, spec, manifest); err == nil {
				t.Fatalf("missing %s accepted", field)
			}
		})
	}
	tests := []struct{ name, requirements string }{
		{"placeholder", "      - unrelated-placeholder\n"},
		{"partial set", "      - REQ-ONE-001\n"},
		{"duplicate", "      - REQ-ONE-001\n      - REQ-ONE-001\n      - REQ-TWO-001\n"},
		{"unknown", "      - REQ-ONE-001\n      - REQ-UNKNOWN-001\n"},
	}
	start := strings.Index(registry, "    requirements:\n") + len("    requirements:\n")
	end := strings.Index(registry[start:], "    compatibility:") + start
	for _, test := range tests {
		test := test
		t.Run("requirements "+test.name, func(t *testing.T) {
			broken := registry[:start] + test.requirements + registry[end:]
			if err := ValidateArtifactRegistry([]byte(broken), manifestData, spec, manifest); err == nil {
				t.Fatalf("artifact requirements %s accepted", test.name)
			}
		})
	}
}

func TestFindCaseRejectsUnknownCaseID(t *testing.T) {
	t.Parallel()
	_, err := FindCase(Manifest{Cases: []Case{{ID: "AC-KNOWN-001"}}}, "AC-UNKNOWN-001")
	if err == nil || !strings.Contains(err.Error(), "unknown acceptance-case ID") {
		t.Fatalf("FindCase() error = %v", err)
	}
}

func TestCheckedInRevisionEvidence(t *testing.T) {
	t.Parallel()
	root := requireLocalScaffolding(t)
	read := func(parts ...string) []byte {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(append([]string{root}, parts...)...))
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	initial := read(".local", "revisions", "SPECIFICATION-28ac242a7c7f4f15.md")
	registered := read(".local", "revisions", "SPECIFICATION-5dc63862dfe786a6.md")
	current := read(".local", "SPECIFICATION.md")
	migrationDiff := read(".local", "revisions", "SPECIFICATION-28ac-to-5dc.diff")
	remediationDiff := read(".local", "revisions", "SPECIFICATION-28ac-to-phase-0-remediation-r1.diff")
	checks := map[string]string{
		sha256Hex(initial):         "28ac242a7c7f4f15bdcc8ad052f504251380580f0749e9f4d257209eb9c61add",
		sha256Hex(registered):      "5dc63862dfe786a6e45cf9155dc56a8ed3cae3769ff80dd135487e35958d5f5d",
		sha256Hex(current):         "ddff3b871cef51d2b137dd3d32e1ae8d57d98813cf2901d901066fa8e435ab3f",
		sha256Hex(migrationDiff):   "27a46e185cc9cf12b10b48c72c7a5e59ba30790527091809b811580f0416a47a",
		sha256Hex(remediationDiff): "f31e3de8e608e4261d6fb10251b67fb56595e9cd6541946196caef730954c82f",
	}
	for got, want := range checks {
		if got != want {
			t.Fatalf("revision evidence hash = %s, want %s", got, want)
		}
	}
	if normalizeSpecificationRevision(registered, false) != string(initial) {
		t.Fatal("registered predecessor contains semantic changes outside stable IDs and worker-workflow identity fields")
	}
	if normalizeSpecificationRevision(current, true) != string(initial) {
		t.Fatal("remediation revision contains semantic changes outside stable IDs and specification-workflow hardening")
	}
}

func normalizeSpecificationRevision(data []byte, remediation bool) string {
	marker := regexp.MustCompile(`\s+<!-- requirement: REQ-[A-Z0-9-]+ -->$`)
	remediationRequirement := regexp.MustCompile(`<!-- requirement: REQ-ACCEPT-WORKFLOW-(?:00[6-9]|01[0-3]) -->`)
	// REQ-PERFORMANCE-011 (minimal, height-aware review) is an additive post-baseline
	// requirement; like the workflow-hardening additions it is stripped so the current
	// revision still reduces byte-for-byte to the immutable 28ac baseline.
	minimalReviewRequirement := regexp.MustCompile(`<!-- requirement: REQ-PERFORMANCE-011 -->`)
	var normalized []string
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "<!-- requirement: REQ-") {
			continue
		}
		if remediation && (remediationRequirement.MatchString(line) || minimalReviewRequirement.MatchString(line) || strings.HasPrefix(line, "The completion proof ")) {
			continue
		}
		line = marker.ReplaceAllString(line, "")
		if remediation && line == "" && len(normalized) > 0 && normalized[len(normalized)-1] == "" {
			continue
		}
		switch line {
		case "References use stable requirement IDs. Section references and exact quoted summaries may accompany an ID for readability, but do not replace it.":
			line = "Until atomic IDs are assigned to all existing requirements, references include both the section and an exact quoted requirement summary."
		case "- `requirements`: stable requirement IDs assigned, with exact quoted summaries when needed for readability;":
			line = "- `clauses`: exact numbered clauses assigned;"
		case "- specification requirement IDs implemented or affected;":
			line = "- contract clauses implemented or affected;"
		}
		normalized = append(normalized, line)
	}
	return strings.Join(normalized, "\n")
}

// TestRevisionLabelBinding binds the manifest's revision label to the exact
// specification hash it denotes. TestCheckedInRevisionEvidence pins the spec
// bytes but asserts nothing about revision_label; Validate only checks that the
// hash matches the spec, not that the label was advanced. Without this, the
// specification can change (a new hash, updated everywhere Validate looks) while
// revision_label silently lags. Pinning the (label, hash) pair here makes the
// label a first-class part of the evidence: any spec-byte change or relabel must
// update BOTH constants together, forcing a deliberate revision decision instead
// of silent drift.
func TestRevisionLabelBinding(t *testing.T) {
	t.Parallel()
	root := requireLocalScaffolding(t)
	_, manifest, err := Load(
		filepath.Join(root, ".local", "SPECIFICATION.md"),
		filepath.Join(root, ".local", "acceptance-manifest.yaml"),
		filepath.Join(root, ".local", "artifacts.yaml"),
	)
	if err != nil {
		t.Fatal(err)
	}
	const boundLabel = "phase-0-remediation-r2"
	const boundSpecHash = "ddff3b871cef51d2b137dd3d32e1ae8d57d98813cf2901d901066fa8e435ab3f"
	if manifest.RevisionLabel != boundLabel || manifest.SpecificationSHA256 != boundSpecHash {
		t.Fatalf("revision binding = (%q, %q), want (%q, %q): a specification change or relabel must update both constants together and consciously choose the revision label",
			manifest.RevisionLabel, manifest.SpecificationSHA256, boundLabel, boundSpecHash)
	}
}

func TestCheckedInManifestValidates(t *testing.T) {
	t.Parallel()
	root := requireLocalScaffolding(t)
	ids, manifest, err := Load(filepath.Join(root, ".local", "SPECIFICATION.md"), filepath.Join(root, ".local", "acceptance-manifest.yaml"), filepath.Join(root, ".local", "artifacts.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) == 0 || len(manifest.Cases) == 0 || len(manifest.Findings) < 5 {
		t.Fatalf("manifest incomplete: requirements=%d cases=%d findings=%d", len(ids), len(manifest.Cases), len(manifest.Findings))
	}
}

func TestCheckedInProofCasesAreBehaviorSpecific(t *testing.T) {
	t.Parallel()
	root := requireLocalScaffolding(t)
	_, manifest, err := Load(filepath.Join(root, ".local", "SPECIFICATION.md"), filepath.Join(root, ".local", "acceptance-manifest.yaml"), filepath.Join(root, ".local", "artifacts.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]Case{}
	manualSignatures := map[string]string{}
	for _, acceptanceCase := range manifest.Cases {
		cases[acceptanceCase.ID] = acceptanceCase
		if acceptanceCase.ProofType != "manual" {
			continue
		}
		if acceptanceCase.BehaviorArea == "" || len(acceptanceCase.RequiredObservations) < 2 || len(acceptanceCase.Steps) < 3 {
			t.Fatalf("manual case %s lacks behavior-specific fields", acceptanceCase.ID)
		}
		if !strings.HasPrefix(acceptanceCase.Steps[0], "Run `") || !strings.HasPrefix(acceptanceCase.Steps[1], "Run `") {
			t.Fatalf("manual case %s does not begin with exact bounded commands", acceptanceCase.ID)
		}
		if !strings.HasPrefix(acceptanceCase.Evidence.Location, ".local/evidence/manual/") || !strings.HasSuffix(acceptanceCase.Evidence.Location, ".json") {
			t.Fatalf("manual case %s lacks a concrete evidence file", acceptanceCase.ID)
		}
		signature := strings.Join(acceptanceCase.Steps, "\n")
		if previous := manualSignatures[signature]; previous != "" {
			t.Fatalf("manual cases %s and %s reuse the same generic procedure", previous, acceptanceCase.ID)
		}
		manualSignatures[signature] = acceptanceCase.ID
	}
	expected := map[string][]string{
		"AC-PROOF-MAPPING":         {"REQ-ACCEPT-WORKFLOW-003", "REQ-ACCEPT-WORKFLOW-004"},
		"AC-HARNESS-FAILURE-MODES": {"REQ-ACCEPT-WORKFLOW-005"},
		"AC-PROOF-SEMANTICS":       {"REQ-ACCEPT-WORKFLOW-010"},
		"AC-CLOSURE-EVIDENCE":      {"REQ-ACCEPT-WORKFLOW-011"},
	}
	for id, requirements := range expected {
		acceptanceCase, ok := cases[id]
		if !ok || !equalStringSets(acceptanceCase.RequirementIDs, requirements) {
			t.Fatalf("case %s mappings = %v, want exact %v", id, acceptanceCase.RequirementIDs, requirements)
		}
	}
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(filename), "..", ".."))
}

// requireLocalScaffolding returns the repository root but skips the calling test
// when the private .local/ governance inputs (the normative spec, manifest,
// artifacts, and frozen revisions) are absent. Those inputs are intentionally
// kept out of the public repository, so they exist only in a full local
// checkout. CI and git worktrees without them skip these checked-in consistency
// gates rather than hard-failing; the gate is still enforced wherever .local/ is
// present (make check). A stat error other than not-exist is a real fault and
// fails the test.
func requireLocalScaffolding(t *testing.T) string {
	t.Helper()
	root := repositoryRoot(t)
	if _, err := os.Stat(filepath.Join(root, ".local", "SPECIFICATION.md")); err != nil {
		if os.IsNotExist(err) {
			t.Skip("acceptance scaffolding (.local/) not present; the checked-in governance gate is enforced where .local/ exists (e.g. make check)")
		}
		t.Fatalf("stat .local/SPECIFICATION.md: %v", err)
	}
	return root
}

func validManifest(spec, predecessor []byte, ids []string) Manifest {
	requirements := make([]Requirement, 0, len(ids))
	for _, id := range ids {
		requirements = append(requirements, Requirement{ID: id, Summary: "test requirement", Status: "active"})
	}
	return Manifest{Schema: Schema, RevisionLabel: "test-r1", SpecificationSHA256: sha256Hex(spec), Predecessor: Predecessor{RevisionLabel: "test-r0", Path: "previous.md", SHA256: sha256Hex(predecessor)}, Producer: "test-worker", Consumers: []string{"reviewer"}, Artifact: Artifact{Path: ".local/acceptance-manifest.yaml", Authority: "executable-evidence", Compatibility: "test", RequirementIDs: append([]string(nil), ids...)}, Requirements: requirements, Cases: []Case{{ID: "AC-VALID-001", RequirementIDs: append([]string(nil), ids...), ProofType: "automated", Command: commandEvidence("AC-VALID-001", "PASS", "combined-output"), ExpectedObservableResult: "PASS", TimeoutSeconds: 5, Prerequisites: []string{}, PermittedSkipConditions: []string{}, Evidence: Evidence{Marker: "PASS", Location: "combined-output"}}}}
}

func cloneManifest(manifest Manifest) Manifest {
	clone := manifest
	clone.Artifact.RequirementIDs = append([]string(nil), manifest.Artifact.RequirementIDs...)
	clone.Requirements = append([]Requirement(nil), manifest.Requirements...)
	clone.Cases = append([]Case(nil), manifest.Cases...)
	for i := range clone.Cases {
		clone.Cases[i].RequirementIDs = append([]string(nil), manifest.Cases[i].RequirementIDs...)
		clone.Cases[i].Steps = append([]string(nil), manifest.Cases[i].Steps...)
		clone.Cases[i].RequiredObservations = append([]string(nil), manifest.Cases[i].RequiredObservations...)
		clone.Cases[i].Prerequisites = append([]string(nil), manifest.Cases[i].Prerequisites...)
		clone.Cases[i].PermittedSkipConditions = append([]string(nil), manifest.Cases[i].PermittedSkipConditions...)
	}
	clone.Findings = append([]Finding(nil), manifest.Findings...)
	return clone
}

func commandEvidence(caseID, marker, location string) string {
	return fmt.Sprintf("printf '%%s\\n' '%s'", EvidenceRecord(caseID, marker, location))
}
func commandSkip(caseID, reason string) string {
	return fmt.Sprintf("printf '%%s\\n' '%s'", SkipRecord(caseID, reason))
}

func artifactRegistry(manifest Manifest, manifestHash, specHash string) string {
	var requirements strings.Builder
	for _, id := range manifest.Artifact.RequirementIDs {
		fmt.Fprintf(&requirements, "      - %s\n", id)
	}
	return fmt.Sprintf("schema: artifact-registry/v1\nspecification_sha256: %s\nartifacts:\n  - name: acceptance-manifest\n    path: %s\n    authority: %s\n    status: active\n    schema: %s\n    sha256: %s\n    specification_sha256: %s\n    producer: %s\n    consumers: [%s]\n    requirements:\n%s    compatibility: %s\n", specHash, manifest.Artifact.Path, manifest.Artifact.Authority, manifest.Schema, manifestHash, specHash, manifest.Producer, strings.Join(manifest.Consumers, ", "), requirements.String(), manifest.Artifact.Compatibility)
}

func validManifestJSON() string {
	digest := sha256.Sum256([]byte("x"))
	return fmt.Sprintf(`{"schema":"%s","revision_label":"r1","specification_sha256":"%s","predecessor":{"revision_label":"r0","path":"p","sha256":"%s"},"producer":"p","consumers":["c"],"artifact":{"path":"p","authority":"a","compatibility":"c"},"requirements":[],"cases":[{"id":"AC-X-001","requirement_ids":[],"proof_type":"automated","command":"x","steps":[],"expected_observable_result":"x","timeout_seconds":5,"prerequisites":[],"permitted_skip_conditions":[],"evidence":{"marker":"x","location":"combined-output"}}],"findings":[]}`, Schema, hex.EncodeToString(digest[:]), hex.EncodeToString(digest[:]))
}
