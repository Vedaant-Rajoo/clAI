package acceptance

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	Schema           = "acceptance-manifest/v1"
	MaxTimeoutSecond = int64(3600)
)

var (
	exactRequirementLine = regexp.MustCompile(`^(.*\S)\s+<!-- requirement: (REQ-[A-Z0-9]+(?:-[A-Z0-9]+)*) -->\s*$`)
	requirementIDLike    = regexp.MustCompile(`(?i)\breq-[a-z0-9][a-z0-9-]*\b`)
	legacySkipOutput     = regexp.MustCompile(`(?im)(?:^\s*---\s+SKIP:|\[skip:)`)
	idPattern            = regexp.MustCompile(`^(?:REQ|AC|P0)-[A-Z0-9]+(?:-[A-Z0-9]+)*$`)
)

type Manifest struct {
	Schema              string        `json:"schema"`
	RevisionLabel       string        `json:"revision_label"`
	SpecificationSHA256 string        `json:"specification_sha256"`
	Predecessor         Predecessor   `json:"predecessor"`
	Producer            string        `json:"producer"`
	Consumers           []string      `json:"consumers"`
	Artifact            Artifact      `json:"artifact"`
	Requirements        []Requirement `json:"requirements"`
	Cases               []Case        `json:"cases"`
	Findings            []Finding     `json:"findings"`
}

type Predecessor struct {
	RevisionLabel string `json:"revision_label"`
	Path          string `json:"path"`
	SHA256        string `json:"sha256"`
}

type Artifact struct {
	Path           string   `json:"path"`
	Authority      string   `json:"authority"`
	Compatibility  string   `json:"compatibility"`
	RequirementIDs []string `json:"requirement_ids"`
}

type Requirement struct {
	ID           string `json:"id"`
	Summary      string `json:"summary"`
	Status       string `json:"status"`
	SupersededBy string `json:"superseded_by"`
}

type Case struct {
	ID                       string   `json:"id"`
	RequirementIDs           []string `json:"requirement_ids"`
	ProofType                string   `json:"proof_type"`
	BehaviorArea             string   `json:"behavior_area"`
	Command                  string   `json:"command"`
	Steps                    []string `json:"steps"`
	RequiredObservations     []string `json:"required_observations"`
	ExpectedObservableResult string   `json:"expected_observable_result"`
	TimeoutSeconds           int64    `json:"timeout_seconds"`
	Prerequisites            []string `json:"prerequisites"`
	PermittedSkipConditions  []string `json:"permitted_skip_conditions"`
	Evidence                 Evidence `json:"evidence"`
}

type Evidence struct {
	Marker   string `json:"marker"`
	Location string `json:"location"`
}

type Finding struct {
	ID                        string                    `json:"id"`
	RequirementIDs            []string                  `json:"requirement_ids"`
	Summary                   string                    `json:"summary"`
	Status                    string                    `json:"status"`
	RequiredRegressionCaseIDs []string                  `json:"required_regression_case_ids"`
	ReviewedRevision          string                    `json:"reviewed_revision"`
	ReplacementRevision       string                    `json:"replacement_revision"`
	IndependentReviewEvidence IndependentReviewEvidence `json:"independent_review_evidence"`
	Closure                   *Closure                  `json:"closure"`
}

type IndependentReviewEvidence struct {
	Required bool   `json:"required"`
	Location string `json:"location"`
	Marker   string `json:"marker"`
}

type Closure struct {
	Status     string `json:"status"`
	Reviewer   string `json:"reviewer"`
	Revision   string `json:"revision"`
	Evidence   string `json:"evidence"`
	RecordedAt string `json:"recorded_at"`
}

type RunResult struct {
	Output     string
	Skipped    bool
	SkipReason string
}

type evidenceRecord struct {
	CaseID   string `json:"case_id"`
	Marker   string `json:"marker"`
	Location string `json:"location"`
}

type skipRecord struct {
	CaseID   string `json:"case_id"`
	Reason   string `json:"reason"`
	ExitCode int    `json:"exit_code"`
}

type artifactEntry struct {
	Path                string
	Authority           string
	Status              string
	Schema              string
	SHA256              string
	SpecificationSHA256 string
	Producer            string
	Consumers           []string
	Compatibility       string
	Requirements        []string
}

type independentReviewRecord struct {
	Schema              string `json:"schema"`
	FindingID           string `json:"finding_id"`
	Status              string `json:"status"`
	Reviewer            string `json:"reviewer"`
	ReviewedRevision    string `json:"reviewed_revision"`
	ReplacementRevision string `json:"replacement_revision"`
	Marker              string `json:"marker"`
	RecordedAt          string `json:"recorded_at"`
}

type manualEvidenceCommand struct {
	Command           string  `json:"command"`
	ExitStatus        int     `json:"exit_status"`
	RawOutputSHA256   string  `json:"raw_output_sha256"`
	RawOutputLocation string  `json:"raw_output_location"`
	DurationSeconds   float64 `json:"duration_seconds"`
	Note              string  `json:"note,omitempty"`
}

type manualObservationOutcome struct {
	Observation string `json:"observation"`
	Outcome     string `json:"outcome"`
	Evidence    string `json:"evidence"`
}

type manualEvidenceRecord struct {
	Schema                      string                     `json:"schema"`
	CaseID                      string                     `json:"case_id"`
	BehaviorArea                string                     `json:"behavior_area"`
	RequirementIDs              []string                   `json:"requirement_ids"`
	RevisionLabel               string                     `json:"revision_label"`
	SpecificationSHA256         string                     `json:"specification_sha256"`
	ReviewedGitRevision         string                     `json:"reviewed_git_revision"`
	WorkingTreeState            string                     `json:"working_tree_state,omitempty"`
	RecordedAt                  string                     `json:"recorded_at"`
	Reviewer                    string                     `json:"reviewer"`
	Commands                    []manualEvidenceCommand    `json:"commands"`
	RequiredObservationOutcomes []manualObservationOutcome `json:"required_observation_outcomes"`
	UnexplainedSkips            string                     `json:"unexplained_skips"`
	Deviations                  string                     `json:"deviations"`
	Marker                      string                     `json:"marker"`
}

func Load(specPath, manifestPath, artifactsPath string) ([]string, Manifest, error) {
	spec, err := os.ReadFile(specPath)
	if err != nil {
		return nil, Manifest{}, fmt.Errorf("read specification: %w", err)
	}
	manifestData, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, Manifest{}, fmt.Errorf("read acceptance manifest: %w", err)
	}
	manifest, err := decodeManifest(manifestData)
	if err != nil {
		return nil, Manifest{}, err
	}
	ids, err := RequirementIDs(spec)
	if err != nil {
		return nil, Manifest{}, err
	}
	root := filepath.Dir(filepath.Dir(manifestPath))
	predecessorPath := manifest.Predecessor.Path
	if !filepath.IsAbs(predecessorPath) {
		predecessorPath = filepath.Join(root, filepath.FromSlash(predecessorPath))
	}
	predecessor, err := os.ReadFile(predecessorPath)
	if err != nil {
		return nil, Manifest{}, fmt.Errorf("read predecessor specification: %w", err)
	}
	predecessorIDs, err := LegacyRequirementIDs(predecessor)
	if err != nil {
		return nil, Manifest{}, fmt.Errorf("parse predecessor specification: %w", err)
	}
	if err := Validate(spec, predecessor, ids, predecessorIDs, manifest); err != nil {
		return nil, Manifest{}, err
	}
	artifactData, err := os.ReadFile(artifactsPath)
	if err != nil {
		return nil, Manifest{}, fmt.Errorf("read artifact registry: %w", err)
	}
	if err := ValidateArtifactRegistry(artifactData, manifestData, spec, manifest); err != nil {
		return nil, Manifest{}, err
	}
	if err := ValidateManualEvidence(root, manifest); err != nil {
		return nil, Manifest{}, err
	}
	if err := ValidateIndependentReviewEvidence(root, manifest); err != nil {
		return nil, Manifest{}, err
	}
	return ids, manifest, nil
}

func decodeManifest(data []byte) (Manifest, error) {
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return Manifest{}, fmt.Errorf("decode acceptance manifest: %w", err)
	}
	var manifest Manifest
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("decode acceptance manifest (JSON syntax is required by acceptance-manifest/v1): %w", err)
	}
	if err := requireJSONEOF(dec); err != nil {
		return Manifest{}, fmt.Errorf("decode acceptance manifest: %w", err)
	}
	return manifest, nil
}

func rejectDuplicateJSONKeys(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var value func() error
	value = func() error {
		token, err := dec.Token()
		if err != nil {
			return err
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for dec.More() {
				keyToken, err := dec.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok {
					return errors.New("object key is not a string")
				}
				if seen[key] {
					return fmt.Errorf("duplicate JSON object key %q", key)
				}
				seen[key] = true
				if err := value(); err != nil {
					return err
				}
			}
			end, err := dec.Token()
			if err != nil {
				return err
			}
			if end != json.Delim('}') {
				return errors.New("malformed JSON object")
			}
		case '[':
			for dec.More() {
				if err := value(); err != nil {
					return err
				}
			}
			end, err := dec.Token()
			if err != nil {
				return err
			}
			if end != json.Delim(']') {
				return errors.New("malformed JSON array")
			}
		default:
			return fmt.Errorf("unexpected JSON delimiter %q", delim)
		}
		return nil
	}
	if err := value(); err != nil {
		return err
	}
	return requireJSONEOF(dec)
}

func requireJSONEOF(dec *json.Decoder) error {
	var trailing any
	err := dec.Decode(&trailing)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("trailing JSON value")
	}
	return fmt.Errorf("trailing JSON data: %w", err)
}

func RequirementIDs(spec []byte) ([]string, error) {
	lines := strings.Split(string(spec), "\n")
	ids := make([]string, 0)
	seen := map[string]bool{}
	for number, line := range lines {
		candidate := hasSuspiciousRequirementComment(line)
		match := exactRequirementLine.FindStringSubmatch(line)
		if candidate && match == nil {
			return nil, fmt.Errorf("malformed requirement marker on line %d", number+1)
		}
		if match == nil {
			continue
		}
		if strings.Count(line, "<!--") != 1 || strings.Count(line, "-->") != 1 {
			return nil, fmt.Errorf("requirement marker on line %d is not a single complete comment", number+1)
		}
		text := substantiveText(match[1])
		if text == "" {
			return nil, fmt.Errorf("requirement marker on line %d is detached from substantive normative text", number+1)
		}
		id := match[2]
		if !idPattern.MatchString(id) || !strings.HasPrefix(id, "REQ-") {
			return nil, fmt.Errorf("invalid requirement ID %q on line %d", id, number+1)
		}
		if seen[id] {
			return nil, fmt.Errorf("duplicate requirement ID %q in specification", id)
		}
		seen[id] = true
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return nil, errors.New("specification contains no stable requirement markers")
	}
	return ids, nil
}

func hasSuspiciousRequirementComment(line string) bool {
	remaining := line
	for {
		start := strings.Index(remaining, "<!--")
		if start < 0 {
			return false
		}
		body := remaining[start+4:]
		end := strings.Index(body, "-->")
		if end < 0 {
			return requirementCommentBodyLike(body)
		}
		if requirementCommentBodyLike(body[:end]) {
			return true
		}
		remaining = body[end+3:]
	}
}

func requirementCommentBodyLike(body string) bool {
	body = strings.TrimSpace(body)
	if requirementIDLike.MatchString(body) {
		return true
	}
	colon := strings.IndexByte(body, ':')
	if colon < 0 {
		return false
	}
	label := strings.ToLower(body[:colon])
	var normalized strings.Builder
	for _, r := range label {
		if r >= 'a' && r <= 'z' {
			normalized.WriteRune(r)
		}
	}
	word := normalized.String()
	if len(word) < 3 {
		return false
	}
	return strings.HasPrefix(word, "req") || strings.HasPrefix("requirement", word) || strings.HasPrefix(word, "requirement") || isSubsequence(word, "requirement")
}

func isSubsequence(candidate, canonical string) bool {
	index := 0
	for _, r := range canonical {
		if index < len(candidate) && byte(r) == candidate[index] {
			index++
		}
	}
	return index == len(candidate)
}

func LegacyRequirementIDs(spec []byte) ([]string, error) {
	legacy := regexp.MustCompile(`<!-- requirement: (REQ-[A-Z0-9]+(?:-[A-Z0-9]+)*) -->`)
	matches := legacy.FindAllSubmatch(spec, -1)
	if len(matches) == 0 {
		return nil, errors.New("predecessor specification contains no registered requirement markers")
	}
	ids := make([]string, 0, len(matches))
	seen := map[string]bool{}
	for _, match := range matches {
		id := string(match[1])
		if seen[id] {
			return nil, fmt.Errorf("duplicate predecessor requirement ID %q", id)
		}
		seen[id] = true
		ids = append(ids, id)
	}
	return ids, nil
}

func substantiveText(text string) string {
	text = strings.TrimSpace(text)
	text = strings.TrimLeft(text, "#*-|. 0123456789\t")
	text = strings.NewReplacer("`", "", "**", "", "_", "").Replace(text)
	for _, r := range text {
		if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			return text
		}
	}
	return ""
}

func Validate(spec, predecessor []byte, specIDs, predecessorIDs []string, manifest Manifest) error {
	var problems []string
	if manifest.Schema != Schema {
		problems = append(problems, fmt.Sprintf("schema must be %q, got %q", Schema, manifest.Schema))
	}
	if strings.TrimSpace(manifest.RevisionLabel) == "" {
		problems = append(problems, "revision_label is required")
	}
	actualSpecHash := sha256Hex(spec)
	if manifest.SpecificationSHA256 != actualSpecHash {
		problems = append(problems, fmt.Sprintf("specification_sha256 mismatch: manifest=%q actual=%q", manifest.SpecificationSHA256, actualSpecHash))
	}
	if manifest.Predecessor.SHA256 != sha256Hex(predecessor) {
		problems = append(problems, fmt.Sprintf("predecessor sha256 mismatch: manifest=%q actual=%q", manifest.Predecessor.SHA256, sha256Hex(predecessor)))
	}
	if manifest.Predecessor.RevisionLabel == "" || manifest.Predecessor.Path == "" {
		problems = append(problems, "predecessor revision_label and path are required")
	}
	if manifest.Producer == "" || len(manifest.Consumers) == 0 || containsBlank(manifest.Consumers) {
		problems = append(problems, "producer and non-empty consumers are required")
	}
	if manifest.Artifact.Path == "" || manifest.Artifact.Authority == "" || manifest.Artifact.Compatibility == "" {
		problems = append(problems, "artifact path, authority, and compatibility are required")
	}

	currentSet := makeSet(specIDs)
	artifactRequirements := map[string]bool{}
	if len(manifest.Artifact.RequirementIDs) == 0 {
		problems = append(problems, "artifact requirement_ids must not be empty")
	}
	for _, id := range manifest.Artifact.RequirementIDs {
		if !validRequirementID(id) {
			problems = append(problems, fmt.Sprintf("artifact requirement_ids contains invalid requirement ID %q", id))
		}
		if artifactRequirements[id] {
			problems = append(problems, fmt.Sprintf("artifact requirement_ids repeats %q", id))
		}
		artifactRequirements[id] = true
		if !currentSet[id] {
			problems = append(problems, fmt.Sprintf("artifact requirement_ids contains unknown current requirement %q", id))
		}
	}
	previousSet := makeSet(predecessorIDs)
	registry := map[string]Requirement{}
	for i, requirement := range manifest.Requirements {
		where := fmt.Sprintf("requirements[%d]", i)
		if !validRequirementID(requirement.ID) {
			problems = append(problems, fmt.Sprintf("%s has invalid requirement ID %q", where, requirement.ID))
		}
		if _, exists := registry[requirement.ID]; exists {
			problems = append(problems, fmt.Sprintf("duplicate requirement ID %q in manifest", requirement.ID))
		}
		registry[requirement.ID] = requirement
		if strings.TrimSpace(requirement.Summary) == "" {
			problems = append(problems, fmt.Sprintf("%s summary is empty", where))
		}
		switch requirement.Status {
		case "active":
			if !currentSet[requirement.ID] {
				problems = append(problems, fmt.Sprintf("active requirement ID %q is absent from current specification", requirement.ID))
			}
			if requirement.SupersededBy != "" {
				problems = append(problems, fmt.Sprintf("active requirement ID %q cannot declare superseded_by", requirement.ID))
			}
		case "retired":
			if currentSet[requirement.ID] {
				problems = append(problems, fmt.Sprintf("retired requirement ID %q remains current", requirement.ID))
			}
		case "superseded":
			if currentSet[requirement.ID] || !validRequirementID(requirement.SupersededBy) {
				problems = append(problems, fmt.Sprintf("superseded requirement ID %q must be absent and name a valid replacement", requirement.ID))
			}
		default:
			problems = append(problems, fmt.Sprintf("%s status must be active, retired, or superseded", where))
		}
	}
	for _, id := range specIDs {
		requirement, ok := registry[id]
		if !ok || requirement.Status != "active" {
			problems = append(problems, fmt.Sprintf("current normative requirement %q is not registered active", id))
		}
	}
	for id := range previousSet {
		if currentSet[id] {
			continue
		}
		requirement, ok := registry[id]
		if !ok || (requirement.Status != "retired" && requirement.Status != "superseded") {
			problems = append(problems, fmt.Sprintf("previously registered requirement %q disappeared without retired or superseded metadata", id))
		}
	}

	caseIDs := map[string]Case{}
	covered := map[string]bool{}
	for i, acceptanceCase := range manifest.Cases {
		where := fmt.Sprintf("cases[%d]", i)
		if !validCaseID(acceptanceCase.ID) {
			problems = append(problems, fmt.Sprintf("%s has invalid acceptance-case ID %q", where, acceptanceCase.ID))
		}
		if _, exists := caseIDs[acceptanceCase.ID]; exists {
			problems = append(problems, fmt.Sprintf("duplicate acceptance-case ID %q", acceptanceCase.ID))
		}
		caseIDs[acceptanceCase.ID] = acceptanceCase
		if len(acceptanceCase.RequirementIDs) == 0 {
			problems = append(problems, fmt.Sprintf("%s covers no requirement IDs", where))
		}
		local := map[string]bool{}
		for _, id := range acceptanceCase.RequirementIDs {
			if local[id] {
				problems = append(problems, fmt.Sprintf("%s repeats requirement ID %q", where, id))
			}
			local[id] = true
			if !currentSet[id] {
				problems = append(problems, fmt.Sprintf("%s references unknown current requirement ID %q", where, id))
			} else {
				covered[id] = true
			}
		}
		if acceptanceCase.TimeoutSeconds <= 0 || acceptanceCase.TimeoutSeconds > MaxTimeoutSecond {
			problems = append(problems, fmt.Sprintf("%s timeout_seconds must be between 1 and %d", where, MaxTimeoutSecond))
		}
		if acceptanceCase.Prerequisites == nil || acceptanceCase.PermittedSkipConditions == nil || containsBlank(acceptanceCase.Prerequisites) || containsBlank(acceptanceCase.PermittedSkipConditions) {
			problems = append(problems, fmt.Sprintf("%s prerequisites and permitted_skip_conditions must be explicit arrays without blank values", where))
		}
		if acceptanceCase.ExpectedObservableResult == "" || acceptanceCase.Evidence.Marker == "" || acceptanceCase.Evidence.Location == "" {
			problems = append(problems, fmt.Sprintf("%s expected result and evidence marker/location are required", where))
		}
		switch acceptanceCase.ProofType {
		case "automated":
			if acceptanceCase.Command == "" || len(acceptanceCase.Steps) != 0 {
				problems = append(problems, fmt.Sprintf("%s automated proof requires command and no manual steps", where))
			}
			if acceptanceCase.Evidence.Location != "combined-output" {
				problems = append(problems, fmt.Sprintf("%s automated evidence location must be combined-output", where))
			}
		case "manual":
			if acceptanceCase.Command != "" || len(acceptanceCase.Steps) < 3 || containsBlank(acceptanceCase.Steps) {
				problems = append(problems, fmt.Sprintf("%s manual proof requires at least three ordered non-empty steps and no command", where))
			}
			if strings.TrimSpace(acceptanceCase.BehaviorArea) == "" || len(acceptanceCase.RequiredObservations) < 2 || containsBlank(acceptanceCase.RequiredObservations) {
				problems = append(problems, fmt.Sprintf("%s manual proof requires a behavior_area and at least two concrete required_observations", where))
			}
			joinedSteps := strings.ToLower(strings.Join(acceptanceCase.Steps, "\n"))
			if strings.Contains(joinedSteps, "inspect the complete product diff against each") || strings.Contains(joinedSteps, "narrowest relevant tests") || strings.Contains(joinedSteps, "inspect all requirements") {
				problems = append(problems, fmt.Sprintf("%s manual proof uses a prohibited generic audit template", where))
			}
			if !strings.HasPrefix(acceptanceCase.Evidence.Location, ".local/evidence/") || !strings.HasSuffix(acceptanceCase.Evidence.Location, ".json") {
				problems = append(problems, fmt.Sprintf("%s manual evidence location must be a concrete .local/evidence/*.json path", where))
			}
		default:
			problems = append(problems, fmt.Sprintf("%s proof_type must be automated or manual", where))
		}
	}
	for _, id := range specIDs {
		if !covered[id] {
			problems = append(problems, fmt.Sprintf("current normative requirement %q has no proof mapping", id))
		}
	}
	validateFindings(manifest, currentSet, caseIDs, &problems)
	if len(problems) > 0 {
		sort.Strings(problems)
		return fmt.Errorf("acceptance manifest invalid:\n- %s", strings.Join(problems, "\n- "))
	}
	return nil
}

func validateFindings(manifest Manifest, requirements map[string]bool, cases map[string]Case, problems *[]string) {
	seen := map[string]bool{}
	for i, finding := range manifest.Findings {
		where := fmt.Sprintf("findings[%d]", i)
		if !strings.HasPrefix(finding.ID, "P0-") || !idPattern.MatchString(finding.ID) {
			*problems = append(*problems, fmt.Sprintf("%s has invalid finding ID %q", where, finding.ID))
		}
		if seen[finding.ID] {
			*problems = append(*problems, fmt.Sprintf("duplicate finding ID %q", finding.ID))
		}
		seen[finding.ID] = true
		if finding.Summary == "" || finding.ReviewedRevision == "" || finding.ReplacementRevision == "" || len(finding.RequirementIDs) == 0 || len(finding.RequiredRegressionCaseIDs) == 0 {
			*problems = append(*problems, fmt.Sprintf("%s requires summary, revisions, requirement IDs, and regression cases", where))
		}
		for _, id := range finding.RequirementIDs {
			if !requirements[id] {
				*problems = append(*problems, fmt.Sprintf("%s references unknown requirement ID %q", where, id))
			}
		}
		for _, id := range finding.RequiredRegressionCaseIDs {
			if _, ok := cases[id]; !ok {
				*problems = append(*problems, fmt.Sprintf("%s references unknown regression case ID %q", where, id))
			}
		}
		if !finding.IndependentReviewEvidence.Required || finding.IndependentReviewEvidence.Location == "" || finding.IndependentReviewEvidence.Marker == "" {
			*problems = append(*problems, fmt.Sprintf("%s requires independent-review evidence metadata", where))
		}
		switch finding.Status {
		case "open":
			if finding.Closure != nil {
				*problems = append(*problems, fmt.Sprintf("%s open finding cannot have closure", where))
			}
		case "fixed-and-conforming", "superseded-by-approved-specification", "accepted-documented-deviation":
			if finding.Closure == nil || finding.Closure.Status != finding.Status || finding.Closure.Reviewer == "" || finding.Closure.Reviewer == manifest.Producer || finding.Closure.Revision != manifest.RevisionLabel || finding.Closure.Evidence != finding.IndependentReviewEvidence.Location || finding.Closure.RecordedAt == "" {
				*problems = append(*problems, fmt.Sprintf("%s closed finding requires matching complete independent closure by a different producer", where))
			} else if _, err := time.Parse(time.RFC3339, finding.Closure.RecordedAt); err != nil {
				*problems = append(*problems, fmt.Sprintf("%s closure recorded_at must be RFC3339", where))
			}
		default:
			*problems = append(*problems, fmt.Sprintf("%s has invalid closure status %q", where, finding.Status))
		}
	}
}

func ValidateManualEvidence(root string, manifest Manifest) error {
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("resolve repository root for manual evidence: %w", err)
	}
	privateRoot := filepath.Join(absoluteRoot, ".local", "evidence")
	manualRoot := filepath.Join(privateRoot, "manual")
	rawRoot := filepath.Join(manualRoot, "raw")
	for _, acceptanceCase := range manifest.Cases {
		if acceptanceCase.ProofType != "manual" {
			continue
		}
		location := acceptanceCase.Evidence.Location
		if filepath.IsAbs(location) || filepath.ToSlash(filepath.Clean(filepath.FromSlash(location))) != location {
			return fmt.Errorf("case %q manual evidence path is not a clean repository-relative path", acceptanceCase.ID)
		}
		path := filepath.Join(absoluteRoot, filepath.FromSlash(location))
		if !pathWithin(manualRoot, path) {
			return fmt.Errorf("case %q manual evidence is outside approved private root .local/evidence/manual", acceptanceCase.ID)
		}
		if err := validatePrivateEvidencePath(privateRoot, path); err != nil {
			return fmt.Errorf("case %q manual evidence: %w", acceptanceCase.ID, err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("case %q read manual evidence: %w", acceptanceCase.ID, err)
		}
		var record manualEvidenceRecord
		if err := decodeStrictRecord(string(data), &record); err != nil {
			return fmt.Errorf("case %q malformed manual evidence: %w", acceptanceCase.ID, err)
		}
		if record.Schema != "manual-acceptance-evidence/v1" || record.CaseID != acceptanceCase.ID || record.BehaviorArea != acceptanceCase.BehaviorArea || !equalStrings(record.RequirementIDs, acceptanceCase.RequirementIDs) || record.RevisionLabel != manifest.RevisionLabel || record.SpecificationSHA256 != manifest.SpecificationSHA256 || record.Marker != acceptanceCase.Evidence.Marker {
			return fmt.Errorf("case %q manual evidence record does not match current manifest metadata", acceptanceCase.ID)
		}
		if strings.TrimSpace(record.Reviewer) == "" || strings.TrimSpace(record.ReviewedGitRevision) == "" || strings.TrimSpace(record.Deviations) == "" {
			return fmt.Errorf("case %q manual evidence requires reviewer, reviewed_git_revision, and deviations", acceptanceCase.ID)
		}
		if _, err := time.Parse(time.RFC3339, record.RecordedAt); err != nil {
			return fmt.Errorf("case %q manual evidence recorded_at must be RFC3339", acceptanceCase.ID)
		}
		if record.UnexplainedSkips != "none" {
			return fmt.Errorf("case %q manual evidence has unexplained skips", acceptanceCase.ID)
		}
		if len(record.Commands) != 2 {
			return fmt.Errorf("case %q manual evidence must contain exactly the first two prescribed commands", acceptanceCase.ID)
		}
		for i := range record.Commands {
			command := commandFromManualStep(acceptanceCase.Steps[i])
			got := record.Commands[i]
			zeroMatchProof := got.ExitStatus == 1 && strings.HasPrefix(got.Command, "rg ") && strings.TrimSpace(got.Note) != ""
			if command == "" || got.Command != command || (got.ExitStatus != 0 && !zeroMatchProof) || got.DurationSeconds < 0 {
				return fmt.Errorf("case %q manual evidence command %d does not match an accepted prescribed outcome", acceptanceCase.ID, i)
			}
			decodedHash, err := hex.DecodeString(got.RawOutputSHA256)
			if err != nil || len(decodedHash) != sha256.Size || got.RawOutputSHA256 != strings.ToLower(got.RawOutputSHA256) {
				return fmt.Errorf("case %q manual evidence command %d has an invalid SHA-256", acceptanceCase.ID, i)
			}
			rawLocation := got.RawOutputLocation
			if filepath.IsAbs(rawLocation) || filepath.ToSlash(filepath.Clean(filepath.FromSlash(rawLocation))) != rawLocation {
				return fmt.Errorf("case %q raw evidence path %d is not clean and repository-relative", acceptanceCase.ID, i)
			}
			rawPath := filepath.Join(absoluteRoot, filepath.FromSlash(rawLocation))
			if !pathWithin(rawRoot, rawPath) {
				return fmt.Errorf("case %q raw evidence path %d is outside .local/evidence/manual/raw", acceptanceCase.ID, i)
			}
			if err := validatePrivateEvidencePath(privateRoot, rawPath); err != nil {
				return fmt.Errorf("case %q raw evidence %d: %w", acceptanceCase.ID, i, err)
			}
			raw, err := os.ReadFile(rawPath)
			if err != nil {
				return fmt.Errorf("case %q read raw evidence %d: %w", acceptanceCase.ID, i, err)
			}
			if sha256Hex(raw) != got.RawOutputSHA256 {
				return fmt.Errorf("case %q raw evidence %d SHA-256 mismatch", acceptanceCase.ID, i)
			}
		}
		if len(record.RequiredObservationOutcomes) != len(acceptanceCase.RequiredObservations) {
			return fmt.Errorf("case %q manual evidence observation count mismatch", acceptanceCase.ID)
		}
		for i, observation := range acceptanceCase.RequiredObservations {
			outcome := record.RequiredObservationOutcomes[i]
			if outcome.Observation != observation || outcome.Outcome != "pass" || strings.TrimSpace(outcome.Evidence) == "" {
				return fmt.Errorf("case %q manual evidence observation %d is not a directly supported pass", acceptanceCase.ID, i)
			}
		}
	}
	return nil
}

func commandFromManualStep(step string) string {
	start := strings.IndexByte(step, '`')
	if start < 0 {
		return ""
	}
	end := strings.IndexByte(step[start+1:], '`')
	if end < 0 {
		return ""
	}
	return step[start+1 : start+1+end]
}

func pathWithin(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func ValidateIndependentReviewEvidence(root string, manifest Manifest) error {
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("resolve repository root for independent review evidence: %w", err)
	}
	privateRoot := filepath.Join(absoluteRoot, ".local", "evidence")
	approvedRoot := filepath.Join(privateRoot, "reviews")
	for _, finding := range manifest.Findings {
		if finding.Status == "open" {
			continue
		}
		location := finding.IndependentReviewEvidence.Location
		if filepath.IsAbs(location) || filepath.ToSlash(filepath.Clean(filepath.FromSlash(location))) != location {
			return fmt.Errorf("finding %q independent evidence path is not a clean repository-relative path", finding.ID)
		}
		path := filepath.Join(absoluteRoot, filepath.FromSlash(location))
		relative, err := filepath.Rel(approvedRoot, path)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return fmt.Errorf("finding %q independent evidence is outside approved private root .local/evidence/reviews", finding.ID)
		}
		if err := validatePrivateEvidencePath(privateRoot, path); err != nil {
			return fmt.Errorf("finding %q independent evidence: %w", finding.ID, err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("finding %q read independent evidence: %w", finding.ID, err)
		}
		var record independentReviewRecord
		if err := decodeStrictRecord(string(data), &record); err != nil {
			return fmt.Errorf("finding %q malformed independent-review evidence: %w", finding.ID, err)
		}
		if record.Schema != "independent-review-evidence/v1" || record.FindingID != finding.ID || record.Status != finding.Status || record.Reviewer != finding.Closure.Reviewer || record.Reviewer == manifest.Producer || record.ReviewedRevision != finding.ReviewedRevision || record.ReplacementRevision != finding.ReplacementRevision || record.Marker != finding.IndependentReviewEvidence.Marker || record.RecordedAt != finding.Closure.RecordedAt {
			return fmt.Errorf("finding %q independent-review evidence record does not match closure metadata", finding.ID)
		}
		if _, err := time.Parse(time.RFC3339, record.RecordedAt); err != nil {
			return fmt.Errorf("finding %q independent-review evidence recorded_at must be RFC3339", finding.ID)
		}
	}
	return nil
}

func validatePrivateEvidencePath(approvedRoot, path string) error {
	targetDirectory := filepath.Dir(path)
	current := approvedRoot
	for {
		info, err := os.Lstat(current)
		if err != nil {
			return fmt.Errorf("approved evidence directory %q: %w", current, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("approved evidence path component %q is not a regular directory", current)
		}
		if info.Mode().Perm()&0o077 != 0 {
			return fmt.Errorf("approved evidence directory %q is not private", current)
		}
		if current == targetDirectory {
			break
		}
		next := nextPathComponent(current, targetDirectory)
		if next == "" {
			return fmt.Errorf("cannot resolve evidence directory path")
		}
		current = filepath.Join(current, next)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("evidence file %q: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("evidence file %q is not a regular non-symlink file", path)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("evidence file %q is not private", path)
	}
	return nil
}

func nextPathComponent(base, target string) string {
	relative, err := filepath.Rel(base, target)
	if err != nil || relative == "." || relative == "" || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return ""
	}
	return strings.Split(relative, string(filepath.Separator))[0]
}

func ValidateArtifactRegistry(registry, manifestData, spec []byte, manifest Manifest) error {
	entry, globalSpecHash, err := parseAcceptanceArtifact(registry)
	if err != nil {
		return err
	}
	var problems []string
	expectedManifestHash := sha256Hex(manifestData)
	expectedSpecHash := sha256Hex(spec)
	checks := []struct{ name, got, want string }{
		{"registry specification_sha256", globalSpecHash, expectedSpecHash},
		{"path", entry.Path, manifest.Artifact.Path},
		{"authority", entry.Authority, manifest.Artifact.Authority},
		{"status", entry.Status, "active"},
		{"schema", entry.Schema, manifest.Schema},
		{"sha256", entry.SHA256, expectedManifestHash},
		{"specification_sha256", entry.SpecificationSHA256, expectedSpecHash},
		{"producer", entry.Producer, manifest.Producer},
		{"compatibility", entry.Compatibility, manifest.Artifact.Compatibility},
	}
	for _, check := range checks {
		if check.got != check.want {
			problems = append(problems, fmt.Sprintf("acceptance-manifest artifact %s mismatch: got %q want %q", check.name, check.got, check.want))
		}
	}
	if !equalStrings(entry.Consumers, manifest.Consumers) {
		problems = append(problems, fmt.Sprintf("acceptance-manifest artifact consumers mismatch: got %v want %v", entry.Consumers, manifest.Consumers))
	}
	knownRequirements := map[string]bool{}
	for _, requirement := range manifest.Requirements {
		if requirement.Status == "active" {
			knownRequirements[requirement.ID] = true
		}
	}
	seenRequirements := map[string]bool{}
	for _, id := range entry.Requirements {
		if seenRequirements[id] {
			problems = append(problems, fmt.Sprintf("acceptance-manifest artifact requirements contains duplicate %q", id))
		}
		seenRequirements[id] = true
		if !validRequirementID(id) || !knownRequirements[id] {
			problems = append(problems, fmt.Sprintf("acceptance-manifest artifact requirements contains unknown requirement %q", id))
		}
	}
	if !equalStringSets(entry.Requirements, manifest.Artifact.RequirementIDs) {
		problems = append(problems, fmt.Sprintf("acceptance-manifest artifact requirements mismatch: got %v want exact set %v", entry.Requirements, manifest.Artifact.RequirementIDs))
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		return fmt.Errorf("artifact registry invalid:\n- %s", strings.Join(problems, "\n- "))
	}
	return nil
}

func parseAcceptanceArtifact(data []byte) (artifactEntry, string, error) {
	lines := strings.Split(string(data), "\n")
	global := ""
	for _, line := range lines {
		if strings.HasPrefix(line, "specification_sha256: ") {
			global = strings.TrimSpace(strings.TrimPrefix(line, "specification_sha256: "))
			break
		}
	}
	start := -1
	for i, line := range lines {
		if line == "  - name: acceptance-manifest" {
			start = i + 1
			break
		}
	}
	if start < 0 {
		return artifactEntry{}, global, errors.New("artifact registry missing acceptance-manifest entry")
	}
	entry := artifactEntry{}
	for i := start; i < len(lines); i++ {
		line := lines[i]
		if strings.HasPrefix(line, "  - name: ") {
			break
		}
		if strings.HasPrefix(line, "      - ") {
			value := strings.TrimSpace(strings.TrimPrefix(line, "      - "))
			value = strings.Trim(value, `"'`)
			entry.Requirements = append(entry.Requirements, value)
			continue
		}
		if !strings.HasPrefix(line, "    ") || strings.HasPrefix(line, "      ") {
			continue
		}
		parts := strings.SplitN(strings.TrimSpace(line), ":", 2)
		if len(parts) != 2 {
			continue
		}
		key, value := parts[0], strings.TrimSpace(parts[1])
		switch key {
		case "path":
			entry.Path = value
		case "authority":
			entry.Authority = value
		case "status":
			entry.Status = value
		case "schema":
			entry.Schema = value
		case "sha256":
			entry.SHA256 = value
		case "specification_sha256":
			entry.SpecificationSHA256 = value
		case "producer":
			entry.Producer = value
		case "consumers":
			value = strings.Trim(value, "[]")
			if value != "" {
				for _, consumer := range strings.Split(value, ",") {
					entry.Consumers = append(entry.Consumers, strings.TrimSpace(consumer))
				}
			}
		case "compatibility":
			entry.Compatibility = value
		}
	}
	return entry, global, nil
}

func FindCase(manifest Manifest, id string) (Case, error) {
	for _, acceptanceCase := range manifest.Cases {
		if acceptanceCase.ID == id {
			return acceptanceCase, nil
		}
	}
	return Case{}, fmt.Errorf("unknown acceptance-case ID %q", id)
}

func RunAutomatedCase(parent context.Context, directory string, acceptanceCase Case) (RunResult, error) {
	if acceptanceCase.ProofType != "automated" {
		return RunResult{}, fmt.Errorf("acceptance case %q is %q, not automated", acceptanceCase.ID, acceptanceCase.ProofType)
	}
	if acceptanceCase.TimeoutSeconds <= 0 || acceptanceCase.TimeoutSeconds > MaxTimeoutSecond {
		return RunResult{}, fmt.Errorf("acceptance case %q has invalid timeout_seconds %d", acceptanceCase.ID, acceptanceCase.TimeoutSeconds)
	}
	ctx, cancel := context.WithTimeout(parent, time.Duration(acceptanceCase.TimeoutSeconds)*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "sh", "-c", acceptanceCase.Command)
	command.Dir = directory
	output, runErr := command.CombinedOutput()
	text := string(output)
	if ctx.Err() == context.DeadlineExceeded {
		return RunResult{Output: text}, fmt.Errorf("acceptance case %q exceeded %ds timeout", acceptanceCase.ID, acceptanceCase.TimeoutSeconds)
	}
	if runErr != nil {
		return RunResult{Output: text}, fmt.Errorf("acceptance case %q exited nonzero: %w", acceptanceCase.ID, runErr)
	}
	if strings.Contains(strings.ToLower(text), "[no tests to run]") {
		return RunResult{Output: text}, fmt.Errorf("acceptance case %q reported [no tests to run]", acceptanceCase.ID)
	}
	evidence, skips, err := parseOutputRecords(text)
	if err != nil {
		return RunResult{Output: text}, fmt.Errorf("acceptance case %q: %w", acceptanceCase.ID, err)
	}
	if len(skips) == 0 && legacySkipOutput.MatchString(text) {
		return RunResult{Output: text}, fmt.Errorf("acceptance case %q emitted skip-like output without an explicit ACCEPTANCE-SKIP record", acceptanceCase.ID)
	}
	if len(skips) > 0 {
		if len(skips) != 1 || len(evidence) != 0 {
			return RunResult{Output: text}, fmt.Errorf("acceptance case %q must emit exactly one skip record and no evidence record", acceptanceCase.ID)
		}
		record := skips[0]
		if record.CaseID != acceptanceCase.ID || record.ExitCode != 0 || !containsExact(acceptanceCase.PermittedSkipConditions, record.Reason) {
			return RunResult{Output: text}, fmt.Errorf("acceptance case %q emitted invalid or undeclared skip record", acceptanceCase.ID)
		}
		return RunResult{Output: text, Skipped: true, SkipReason: record.Reason}, nil
	}
	if len(evidence) != 1 {
		return RunResult{Output: text}, fmt.Errorf("acceptance case %q must emit exactly one evidence record, got %d", acceptanceCase.ID, len(evidence))
	}
	record := evidence[0]
	if record.CaseID != acceptanceCase.ID || record.Marker != acceptanceCase.Evidence.Marker || record.Location != acceptanceCase.Evidence.Location {
		return RunResult{Output: text}, fmt.Errorf("acceptance case %q evidence record mismatch", acceptanceCase.ID)
	}
	return RunResult{Output: text}, nil
}

func parseOutputRecords(output string) ([]evidenceRecord, []skipRecord, error) {
	var evidence []evidenceRecord
	var skips []skipRecord
	for _, line := range strings.Split(output, "\n") {
		switch {
		case strings.HasPrefix(line, "ACCEPTANCE-EVIDENCE"):
			if !strings.HasPrefix(line, "ACCEPTANCE-EVIDENCE ") {
				return nil, nil, errors.New("malformed ACCEPTANCE-EVIDENCE record")
			}
			var record evidenceRecord
			if err := decodeStrictRecord(strings.TrimPrefix(line, "ACCEPTANCE-EVIDENCE "), &record); err != nil {
				return nil, nil, fmt.Errorf("malformed ACCEPTANCE-EVIDENCE record: %w", err)
			}
			evidence = append(evidence, record)
		case strings.HasPrefix(line, "ACCEPTANCE-SKIP"):
			if !strings.HasPrefix(line, "ACCEPTANCE-SKIP ") {
				return nil, nil, errors.New("malformed ACCEPTANCE-SKIP record")
			}
			var record skipRecord
			if err := decodeStrictRecord(strings.TrimPrefix(line, "ACCEPTANCE-SKIP "), &record); err != nil {
				return nil, nil, fmt.Errorf("malformed ACCEPTANCE-SKIP record: %w", err)
			}
			skips = append(skips, record)
		}
	}
	return evidence, skips, nil
}

func decodeStrictRecord(data string, target any) error {
	raw := []byte(data)
	if err := rejectDuplicateJSONKeys(raw); err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(target); err != nil {
		return err
	}
	return requireJSONEOF(dec)
}

func validRequirementID(id string) bool {
	return strings.HasPrefix(id, "REQ-") && idPattern.MatchString(id)
}
func validCaseID(id string) bool { return strings.HasPrefix(id, "AC-") && idPattern.MatchString(id) }
func makeSet(values []string) map[string]bool {
	result := map[string]bool{}
	for _, v := range values {
		result[v] = true
	}
	return result
}
func containsExact(values []string, target string) bool {
	for _, v := range values {
		if v == target {
			return true
		}
	}
	return false
}
func containsBlank(values []string) bool {
	for _, v := range values {
		if strings.TrimSpace(v) == "" {
			return true
		}
	}
	return false
}
func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
func equalStringSets(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	counts := map[string]int{}
	for _, value := range a {
		counts[value]++
	}
	for _, value := range b {
		counts[value]--
	}
	for _, count := range counts {
		if count != 0 {
			return false
		}
	}
	return true
}
func sha256Hex(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func EvidenceRecord(caseID, marker, location string) string {
	data, _ := json.Marshal(evidenceRecord{CaseID: caseID, Marker: marker, Location: location})
	return "ACCEPTANCE-EVIDENCE " + string(data)
}

func SkipRecord(caseID, reason string) string {
	data, _ := json.Marshal(skipRecord{CaseID: caseID, Reason: reason, ExitCode: 0})
	return "ACCEPTANCE-SKIP " + string(data)
}

func ParseTimeoutLiteral(value string) (int64, error) {
	seconds, err := strconv.ParseInt(value, 10, 64)
	if err != nil || seconds <= 0 || seconds > MaxTimeoutSecond {
		return 0, fmt.Errorf("timeout_seconds must be a whole number between 1 and %d", MaxTimeoutSecond)
	}
	return seconds, nil
}
