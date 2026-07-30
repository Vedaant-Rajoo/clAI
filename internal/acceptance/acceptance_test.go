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

func TestValidateManualEvidenceFiles(t *testing.T) {
	setup := func(t *testing.T) (string, Manifest, string, string, manualEvidenceRecord) {
		t.Helper()
		root := t.TempDir()
		rawDir := filepath.Join(root, ".local", "evidence", "manual", "raw")
		if err := os.MkdirAll(rawDir, 0o700); err != nil {
			t.Fatal(err)
		}
		for _, dir := range []string{
			filepath.Join(root, ".local", "evidence"),
			filepath.Join(root, ".local", "evidence", "manual"),
			rawDir,
		} {
			if err := os.Chmod(dir, 0o700); err != nil {
				t.Fatal(err)
			}
		}
		rawLocation := ".local/evidence/manual/raw/AC-MANUAL-TEST-0.log"
		rawPath := filepath.Join(root, filepath.FromSlash(rawLocation))
		raw := []byte("proof output\n")
		if err := os.WriteFile(rawPath, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		secondLocation := ".local/evidence/manual/raw/AC-MANUAL-TEST-1.log"
		secondPath := filepath.Join(root, filepath.FromSlash(secondLocation))
		if err := os.WriteFile(secondPath, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		acceptanceCase := Case{
			ID: "AC-MANUAL-TEST", RequirementIDs: []string{"REQ-TEST-001"}, ProofType: "manual",
			BehaviorArea: "test behavior", Steps: []string{"Run `go test ./...`.", "Run `rg proof internal`."},
			RequiredObservations: []string{"First observation", "Second observation"},
			Evidence:             Evidence{Marker: "MANUAL-TEST-PASS", Location: ".local/evidence/manual/AC-MANUAL-TEST.json"},
		}
		manifest := Manifest{RevisionLabel: "test-r1", SpecificationSHA256: strings.Repeat("a", 64), Cases: []Case{acceptanceCase}}
		record := manualEvidenceRecord{
			Schema: "manual-acceptance-evidence/v1", CaseID: acceptanceCase.ID, BehaviorArea: acceptanceCase.BehaviorArea,
			RequirementIDs: acceptanceCase.RequirementIDs, RevisionLabel: manifest.RevisionLabel,
			SpecificationSHA256: manifest.SpecificationSHA256, ReviewedGitRevision: "deadbeef", RecordedAt: "2026-07-24T12:00:00Z",
			Reviewer: "fresh-reviewer", UnexplainedSkips: "none", Deviations: "none", Marker: acceptanceCase.Evidence.Marker,
			Commands: []manualEvidenceCommand{
				{Command: "go test ./...", ExitStatus: 0, RawOutputSHA256: sha256Hex(raw), RawOutputLocation: rawLocation, DurationSeconds: 1},
				{Command: "rg proof internal", ExitStatus: 0, RawOutputSHA256: sha256Hex(raw), RawOutputLocation: secondLocation, DurationSeconds: 1},
			},
			RequiredObservationOutcomes: []manualObservationOutcome{
				{Observation: "First observation", Outcome: "pass", Evidence: "TestOne proves it."},
				{Observation: "Second observation", Outcome: "pass", Evidence: "TestTwo proves it."},
			},
		}
		return root, manifest, filepath.Join(root, filepath.FromSlash(acceptanceCase.Evidence.Location)), rawPath, record
	}
	write := func(t *testing.T, path string, record manualEvidenceRecord, mode os.FileMode) {
		t.Helper()
		data, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, mode); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("valid current record", func(t *testing.T) {
		root, manifest, path, _, record := setup(t)
		write(t, path, record, 0o600)
		if err := ValidateManualEvidence(root, manifest); err != nil {
			t.Fatal(err)
		}
	})
	for _, test := range []struct {
		name string
		edit func(*manualEvidenceRecord)
	}{
		{"stale revision", func(r *manualEvidenceRecord) { r.RevisionLabel = "old" }},
		{"stale specification hash", func(r *manualEvidenceRecord) { r.SpecificationSHA256 = strings.Repeat("b", 64) }},
		{"wrong marker", func(r *manualEvidenceRecord) { r.Marker = "wrong" }},
		{"wrong command", func(r *manualEvidenceRecord) { r.Commands[0].Command = "true" }},
		{"failed command", func(r *manualEvidenceRecord) { r.Commands[0].ExitStatus = 1 }},
		{"wrong raw hash", func(r *manualEvidenceRecord) { r.Commands[0].RawOutputSHA256 = strings.Repeat("0", 64) }},
		{"unmapped observation", func(r *manualEvidenceRecord) { r.RequiredObservationOutcomes[0].Observation = "other" }},
		{"failed observation", func(r *manualEvidenceRecord) { r.RequiredObservationOutcomes[0].Outcome = "fail" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			root, manifest, path, _, record := setup(t)
			test.edit(&record)
			write(t, path, record, 0o600)
			if err := ValidateManualEvidence(root, manifest); err == nil {
				t.Fatal("invalid manual evidence accepted")
			}
		})
	}
	t.Run("non-private record", func(t *testing.T) {
		root, manifest, path, _, record := setup(t)
		write(t, path, record, 0o644)
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := ValidateManualEvidence(root, manifest); err == nil {
			t.Fatal("non-private manual evidence accepted")
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

func TestProviderBoundaryArtifactsRegistered(t *testing.T) {
	t.Parallel()
	root := requireLocalScaffolding(t)
	data, err := os.ReadFile(filepath.Join(root, ".local", "artifacts.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	registry := string(data)
	for _, required := range []string{
		"name: candidate-json-contract-v1",
		"path: .local/revisions/CANDIDATEJSON-d4ebadb8f0d525bc.go",
		"status: superseded-by-candidate-v2",
		"schema: candidate/v1",
		"sha256: d4ebadb8f0d525bcaef836114f5c2ebf7906be59c8268a2a05747205e5903ecb",
		"name: candidate-json-contract-v2",
		"schema: candidate/v2",
		"name: request-receipt-contract",
		"schema: request-receipt/v1",
		"name: capability-inventory-contract",
		"schema: capability-inventory/v1",
		"name: context-capsule-contract-v2",
		"schema: context-capsule/v2",
		"name: context-selector-contract-v2",
		"schema: context-selector/v2",
		"name: candidate-applicability-contract",
		"schema: candidate-applicability/v1",
		"name: capability-conditioned-fixtures",
		"schema: capability-fixtures/v1",
		"name: specification-predecessor-1541",
		"path: .local/revisions/SPECIFICATION-1541a84cbe057e42.md",
		"name: specification-semantic-diff-1541-to-phase-b-xdg-config-r7",
		"path: .local/revisions/SPECIFICATION-1541-to-phase-b-xdg-config-r7.diff",
		"name: config-root-policy",
		"name: config-file-contract-v1",
		"schema: config-file/v1",
		"name: credential-file-storage-contract",
		"REQ-ANTHROPIC-011",
		"REQ-ANTHROPIC-010",
		"REQ-CONTEXT-031",
		"REQ-OPENROUTER-001",
	} {
		if !strings.Contains(registry, required) {
			t.Errorf("artifact registry missing %q", required)
		}
	}

	type registeredFileArtifact struct {
		name   string
		path   string
		sha256 string
	}
	var registered []registeredFileArtifact
	var current registeredFileArtifact
	flush := func() {
		if current.name != "" {
			registered = append(registered, current)
		}
		current = registeredFileArtifact{}
	}
	for _, line := range strings.Split(registry, "\n") {
		switch {
		case strings.HasPrefix(line, "  - name: "):
			flush()
			current.name = strings.TrimPrefix(line, "  - name: ")
		case strings.HasPrefix(line, "    path: "):
			current.path = strings.TrimPrefix(line, "    path: ")
		case strings.HasPrefix(line, "    sha256: "):
			current.sha256 = strings.TrimPrefix(line, "    sha256: ")
		}
	}
	flush()
	for _, artifact := range registered {
		if artifact.path == "" || artifact.sha256 == "" {
			continue
		}
		content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(artifact.path)))
		if err != nil {
			t.Errorf("registered artifact %s path %s: %v", artifact.name, artifact.path, err)
			continue
		}
		if got := sha256Hex(content); got != artifact.sha256 {
			t.Errorf("registered artifact %s hash = %s, want %s for %s", artifact.name, got, artifact.sha256, artifact.path)
		}
	}

	contentHashes := map[string]string{
		".local/SPECIFICATION.md":                                           "27fcdfd464be41ff581d81889f2bffd1123b9eeb2f1431214f02393ee0401fc5",
		".local/acceptance-manifest.yaml":                                   "54d9bbcaae6bab48a512459542ad7cea3f87859d0c38de6fa5d87b51fb148781",
		".local/revisions/SPECIFICATION-1541a84cbe057e42.md":                "1541a84cbe057e422d2fa83421e8f7a4241c85e93c50ed702fd4a9ed15a1512f",
		".local/revisions/SPECIFICATION-1541-to-phase-b-xdg-config-r7.diff": "d2668a3d07d2edd1e79be95c261dcfc168140b464022b62dfd98a90976153217",
		"internal/configroot/configroot.go":                                 "ef45539acc6b4803aafde39dd58755c2bde1592d38345a18752acb23343d26ad",
		"internal/configroot/filesystem_unix.go":                            "637eb181794152a1639bc44fc856703cd462523896bf5514e7d5a93dff31eb1a",
		"internal/configroot/filesystem_other.go":                           "fa2f39d13dea9d496dcb90a41883c1c616792c667a4322ca8519f4420517f712",
		"internal/config/config.go":                                         "cb0eee1219efe41b9c62c5fb986cec369f24fe114833e9f32d172a2d4c8a7382",
		"internal/config/filesystem_unix.go":                                "f40e7598b0704e358b931ccc997e14e64bb8d9c1cd666af2fe5768a185bcb18a",
		"internal/config/filesystem_other.go":                               "5e8fc77c90cb010b66ebb7fb6627745a693f8402227d0d69c5dbc2bb8ea19c5e",
		"internal/auth/auth.go":                                             "79bb211d63ffeecf3143a303a89912a58d44f0f0335d35245f88083232cd93e5",
		"internal/auth/filesystem_unix.go":                                  "29b846261f18625b5cbe549dd37802c5421928d54db8f69c956bfb6bd760e172",
		"internal/auth/filesystem_other.go":                                 "1617a7b64e905c6afbb4280801c13c46614334af8553f3350f83c8de4da042ea",
		".local/revisions/CANDIDATEJSON-d4ebadb8f0d525bc.go":                "d4ebadb8f0d525bcaef836114f5c2ebf7906be59c8268a2a05747205e5903ecb",
		"internal/provider/candidatejson/candidatejson.go":                  "7ff6307b4c4a3319fb7019b0e04cd15399c9cfa2499358f5d9fa701b00cfea9b",
		"internal/provider/receipt.go":                                      "16024cc7e6f6234c4ccfab0813a5dec768351a799f0c61e71823dc4c2bcfeaa0",
		"internal/capability/capability.go":                                 "0151856bf5a6611a40fb74e6b580d0603587af5ef1a81fad711c384ad51a2930",
		"internal/capability/allowlist.go":                                  "278085169c7b742713b6c5b0b7067ecc4635d1c06747de01038fafb2e9ace18a",
		"internal/capability/collect.go":                                    "5c14ac99a0be51895075ec867ed692292224aa98763b2d855aa1c6fcf50c92a1",
		"internal/context/privacy.go":                                       "ace41ad7edd1f18e5866053f97cd85e7f367811bd500f305e43fbf27cb4eb8d9",
		"internal/applicability/applicability.go":                           "6d004826ee2d9b20a06ba328994d648358a4fe7c9e4b46884e7ce1c49c2d1318",
		"internal/applicability/edit.go":                                    "563651d2da8c3e416a09867d1aacb61a7f3d72994a763dd8cf41e9023717e0a4",
		"integration/testdata/capability-fixtures.json":                     "8435bb0ff4ca545e700fffe476adfdc6833ad5fa0258471ecddccd15474b0806",
		"internal/provider/rules/rules_test.go":                             "57963925307cc017b10e28c58e540ff6190e8fa0c8cedffd20d89b5b205afd45",
		"integration/capability_fixture_test.go":                            "63c4a25a9839aa7909be331bde76ea51bbebb9e71fe5bef66aa0c5843a549063",
	}
	for path, want := range contentHashes {
		content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
		if err != nil {
			t.Fatal(err)
		}
		if got := sha256Hex(content); got != want {
			t.Errorf("artifact content hash %s = %s, want %s", path, got, want)
		}
		if !strings.Contains(registry, want) {
			t.Errorf("artifact registry missing content hash for %s", path)
		}
	}

	bundleHashes := []struct {
		paths []string
		want  string
	}{
		{[]string{"internal/capability/capability.go", "internal/capability/allowlist.go", "internal/capability/collect.go"}, "f3981779d5940213ab1aa5fcbea18c698f9c94118fa7acd53c202b86a645c2b7"},
		{[]string{"internal/applicability/applicability.go", "internal/applicability/edit.go"}, "b4d7f837f42ba3f59908cbe80b1ae5b1a994894e31a2d1e86f5db582cff576b5"},
		{[]string{"integration/testdata/capability-fixtures.json", "internal/provider/rules/rules_test.go", "integration/capability_fixture_test.go"}, "0c47b2728ef6a8029b7ba7721ae87e6c84981cc8e82da06e2a78c98eab649c55"},
		{[]string{"internal/configroot/configroot.go", "internal/configroot/filesystem_unix.go", "internal/configroot/filesystem_other.go"}, "11c1c5eb56439702763ac184ca57d3083e20796c022243b0f53c0f84a802d158"},
		{[]string{"internal/config/config.go", "internal/config/filesystem_unix.go", "internal/config/filesystem_other.go"}, "7fde34731b9e2a83fc9f4d5426459e0ab8c7eddc9c257bf06daefdfbcdb37cec"},
		{[]string{"internal/auth/auth.go", "internal/auth/filesystem_unix.go", "internal/auth/filesystem_other.go"}, "27470fa9422d731c7bd1b306ebc27fc35470c6e417737e217a47ebb85d811db8"},
	}
	for _, bundle := range bundleHashes {
		hash := sha256.New()
		for _, path := range bundle.paths {
			content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
			if err != nil {
				t.Fatal(err)
			}
			hash.Write([]byte(path))
			hash.Write([]byte{0})
			hash.Write(content)
			hash.Write([]byte{0})
		}
		if got := hex.EncodeToString(hash.Sum(nil)); got != bundle.want {
			t.Errorf("artifact bundle hash %v = %s, want %s", bundle.paths, got, bundle.want)
		}
		if !strings.Contains(registry, bundle.want) {
			t.Errorf("artifact registry missing bundle hash for %v", bundle.paths)
		}
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
	predecessor := read(".local", "revisions", "SPECIFICATION-ddff3b871cef51d2.md")
	phaseA := read(".local", "revisions", "SPECIFICATION-f6a55193c3b420ec.md")
	phaseBR1 := read(".local", "revisions", "SPECIFICATION-23a08cc1161da026.md")
	phaseBR2 := read(".local", "revisions", "SPECIFICATION-12c6d5f823978edd.md")
	phaseBR3 := read(".local", "revisions", "SPECIFICATION-514f7d5c2881b0d1.md")
	phaseBR4 := read(".local", "revisions", "SPECIFICATION-f8816bbf262963ab.md")
	phaseBR5 := read(".local", "revisions", "SPECIFICATION-4a9ec1befd556a23.md")
	phaseBR6 := read(".local", "revisions", "SPECIFICATION-1541a84cbe057e42.md")
	current := read(".local", "SPECIFICATION.md")
	migrationDiff := read(".local", "revisions", "SPECIFICATION-28ac-to-5dc.diff")
	remediationDiff := read(".local", "revisions", "SPECIFICATION-28ac-to-phase-0-remediation-r1.diff")
	anthropicDiff := read(".local", "revisions", "SPECIFICATION-ddff-to-phase-a-anthropic-r1.diff")
	phaseBR1Diff := read(".local", "revisions", "SPECIFICATION-f6a-to-phase-b-applicability-r1.diff")
	phaseBR2Diff := read(".local", "revisions", "SPECIFICATION-23a-to-phase-b-applicability-r2.diff")
	phaseBR3Diff := read(".local", "revisions", "SPECIFICATION-12c-to-phase-b-applicability-r3.diff")
	phaseBR4Diff := read(".local", "revisions", "SPECIFICATION-514f-to-phase-b-applicability-r4.diff")
	phaseBR5Diff := read(".local", "revisions", "SPECIFICATION-f881-to-phase-b-applicability-r5.diff")
	phaseBR6Diff := read(".local", "revisions", "SPECIFICATION-4a9e-to-phase-b-devendpoint-r6.diff")
	phaseBR7Diff := read(".local", "revisions", "SPECIFICATION-1541-to-phase-b-xdg-config-r7.diff")
	checks := map[string]string{
		sha256Hex(initial):         "28ac242a7c7f4f15bdcc8ad052f504251380580f0749e9f4d257209eb9c61add",
		sha256Hex(registered):      "5dc63862dfe786a6e45cf9155dc56a8ed3cae3769ff80dd135487e35958d5f5d",
		sha256Hex(predecessor):     "ddff3b871cef51d2b137dd3d32e1ae8d57d98813cf2901d901066fa8e435ab3f",
		sha256Hex(phaseA):          "f6a55193c3b420ec28a68c050fb7be8a541753ee8a33b6c5900c9cef6ab5a676",
		sha256Hex(phaseBR1):        "23a08cc1161da026da703a2e807dd1b2bd5d4c59015ab799732b47a819dd8c23",
		sha256Hex(phaseBR2):        "12c6d5f823978eddf65efac9e07b9b90e73df8d3360373099698d80526017003",
		sha256Hex(phaseBR3):        "514f7d5c2881b0d18cc18e87584324dafd1bd07a484195e78342599cba372d5e",
		sha256Hex(phaseBR4):        "f8816bbf262963ab38b484eecb5ec17283f57de5aaa540c08608cf2f41a85616",
		sha256Hex(phaseBR5):        "4a9ec1befd556a231489d6a4e4b549d08ce6810df1f49782913e71ed6eca0481",
		sha256Hex(phaseBR6):        "1541a84cbe057e422d2fa83421e8f7a4241c85e93c50ed702fd4a9ed15a1512f",
		sha256Hex(current):         "27fcdfd464be41ff581d81889f2bffd1123b9eeb2f1431214f02393ee0401fc5",
		sha256Hex(migrationDiff):   "27a46e185cc9cf12b10b48c72c7a5e59ba30790527091809b811580f0416a47a",
		sha256Hex(remediationDiff): "f31e3de8e608e4261d6fb10251b67fb56595e9cd6541946196caef730954c82f",
		sha256Hex(anthropicDiff):   "dae1358bbac67a249050a46767dea16a4406a262889a6d83a454c68a3d2d6601",
		sha256Hex(phaseBR1Diff):    "e7b44cce933da8242aea4d853378be457a1d832fca8693e5e1b697a3b81cf018",
		sha256Hex(phaseBR2Diff):    "e9545bb13d9ad1b890ad6000b979dbb3780d1c7770d0cec047298ca926de78af",
		sha256Hex(phaseBR3Diff):    "d966929ea13c24b17b10cf98da579578850d0537b7e66b13b244f61736a5e8a5",
		sha256Hex(phaseBR4Diff):    "b97413cc9387c63d108ed366a62a418c951921f71ea84f6f47839b1957eed201",
		sha256Hex(phaseBR5Diff):    "0c5cc7de643aa4eba438d9cc828e02a98c4320d8313b305c0a59de4b6b744bac",
		sha256Hex(phaseBR6Diff):    "c6db712309d375431713d31aa7a389224bc28c82678fb974ddf6ecbe3293b7c5",
		sha256Hex(phaseBR7Diff):    "d2668a3d07d2edd1e79be95c261dcfc168140b464022b62dfd98a90976153217",
	}
	for got, want := range checks {
		if got != want {
			t.Fatalf("revision evidence hash = %s, want %s", got, want)
		}
	}
	if normalizeSpecificationRevision(registered, false) != string(initial) {
		t.Fatal("registered predecessor contains semantic changes outside stable IDs and worker-workflow identity fields")
	}
	// The phase-0-remediation-r2 predecessor (ddff) is the last revision whose
	// normative bytes still reduce to the immutable 28ac assignment-start baseline
	// after stripping requirement-ID markers and controlled workflow-hardening
	// additions. Phase A deliberately diverges from that baseline by adding the
	// direct Anthropic provider tranche. The immutable Phase A snapshot and every
	// Phase B r1..r6 predecessor is therefore bound to the next revision by the
	// registered semantic diffs pinned above; TestRevisionLabelBinding additionally
	// binds the current r7 bytes to the r7 label.
	if normalizeSpecificationRevision(predecessor, true) != string(initial) {
		t.Fatal("registered phase-0-remediation-r2 predecessor contains semantic changes outside stable IDs and specification-workflow hardening")
	}
}

func normalizeSpecificationRevision(data []byte, remediation bool) string {
	marker := regexp.MustCompile(`\s+<!-- requirement: REQ-[A-Z0-9-]+ -->$`)
	remediationRequirement := regexp.MustCompile(`<!-- requirement: REQ-ACCEPT-WORKFLOW-(?:00[6-9]|01[0-3]) -->`)
	// REQ-PERFORMANCE-011 (minimal, height-aware review) is an additive post-baseline
	// requirement; like the workflow-hardening additions it is stripped so the predecessor
	// revision (the sole remediation=true caller below) still reduces byte-for-byte to the
	// immutable 28ac baseline.
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
	const boundLabel = "phase-b-xdg-config-r7"
	const boundSpecHash = "27fcdfd464be41ff581d81889f2bffd1123b9eeb2f1431214f02393ee0401fc5"
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
	if strings.Contains(manifest.Artifact.Compatibility, "keeps prior rejection findings open") || !strings.Contains(manifest.Artifact.Compatibility, "recorded fixed-and-conforming closures") {
		t.Fatalf("manifest compatibility misstates retained finding closure state: %q", manifest.Artifact.Compatibility)
	}
	retained := map[string]bool{"P0-PHASEB-001": true, "P0-PHASEB-002": true, "P0-PHASEB-003": true}
	for _, finding := range manifest.Findings {
		if retained[finding.ID] && finding.Status != "fixed-and-conforming" {
			t.Fatalf("retained Phase B finding %s status = %q, want fixed-and-conforming", finding.ID, finding.Status)
		}
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
