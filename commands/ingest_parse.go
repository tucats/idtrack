package commands

import (
	"path/filepath"
	"regexp"
	"strings"

	"github.com/tucats/idtrack/db"
)

// section is one boundary-delimited chunk of an ingested file, produced by
// splitSections. Every section after the first becomes a comment on the
// created issue; the first is the issue description.
type section struct {
	Label string // the header/label text found on the boundary line, lower-cased comparisons use this
	Body  string // the full raw text of the section, including its own boundary line
}

var (
	h1Pattern         = regexp.MustCompile(`^#\s+(.+)$`)
	headerPattern     = regexp.MustCompile(`^#{2,6}\s+(.+?)\s*$`)
	boldLabelPattern  = regexp.MustCompile(`^\*{2,3}([^*\n]{1,60}?):?\*{2,3}`)
	plainLabelPattern = regexp.MustCompile(`^([A-Z][A-Za-z0-9 /_-]{1,40}):(\s|$)`)
	fenceLinePattern  = regexp.MustCompile("^```")
	severityCodeToken = regexp.MustCompile(`^[hmlc]\d{1,2}$`)
	nonAlnumPattern   = regexp.MustCompile(`[^a-z0-9]+`)

	highPriorityPattern   = regexp.MustCompile(`(?i)\b(critical|high)\b`)
	mediumPriorityPattern = regexp.MustCompile(`(?i)\bmedium\b`)
	lowPriorityPattern    = regexp.MustCompile(`(?i)\b(low|minor)\b`)

	resolvedWordPattern = regexp.MustCompile(`(?i)\b(resolved|fixed|closed)\b`)
	statusMarkerPattern = regexp.MustCompile(`(?i)\bstatus\b[^\n]{0,20}\b(resolved|fixed|closed)\b`)

	codeFencePattern = regexp.MustCompile("(?s)```.*?```")
)

// extractTitle implements rule 2 of the ingest spec: use the first line if it
// is a "#" markdown title, otherwise fall back to the first full sentence of
// the text (delimited by ".").
func extractTitle(content string) string {
	lines := strings.Split(content, "\n")

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}

		if m := h1Pattern.FindStringSubmatch(trimmed); m != nil {
			return strings.TrimSpace(m[1])
		}

		break
	}

	return firstSentence(content)
}

// firstSentence returns the first sentence of the first paragraph of content,
// skipping fenced code blocks. If no sentence terminator is found the text is
// truncated to a reasonable title length instead.
func firstSentence(content string) string {
	text := codeFencePattern.ReplaceAllString(content, " ")
	text = strings.TrimSpace(text)

	if idx := strings.Index(text, "\n\n"); idx >= 0 {
		text = text[:idx]
	}

	text = strings.Join(strings.Fields(text), " ")

	if idx := strings.Index(text, ". "); idx >= 0 {
		return strings.TrimSpace(text[:idx+1])
	}

	if strings.HasSuffix(text, ".") {
		return text
	}

	const maxLen = 120
	if len(text) > maxLen {
		return strings.TrimSpace(text[:maxLen]) + "…"
	}

	return text
}

// splitSections implements rule 4: the text before the first detected section
// boundary is the issue description; each boundary after that starts a new
// comment running to the next boundary (or end of file). A leading "#" title
// line is skipped so it is not duplicated into the description. Boundaries
// are recognized as markdown ## headers, "**Label:**" bold pseudo-headers (the
// dominant convention in the sample corpus), or plain "Label:" lines — all
// ignored while inside a fenced code block.
func splitSections(content string) (description string, comments []section) {
	lines := strings.Split(content, "\n")

	start := 0
	for start < len(lines) {
		trimmed := strings.TrimSpace(lines[start])
		if trimmed == "" {
			start++

			continue
		}

		if h1Pattern.MatchString(trimmed) {
			start++
		}

		break
	}

	type boundary struct {
		label string
		line  int
	}

	var boundaries []boundary

	inFence := false

	for i := start; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])

		if fenceLinePattern.MatchString(trimmed) {
			inFence = !inFence

			continue
		}

		if inFence {
			continue
		}

		if label, ok := boundaryLabel(trimmed); ok {
			boundaries = append(boundaries, boundary{label, i})
		}
	}

	if len(boundaries) == 0 {
		return strings.TrimSpace(strings.Join(lines[start:], "\n")), nil
	}

	preText := strings.TrimSpace(strings.Join(lines[start:boundaries[0].line], "\n"))

	allSections := make([]section, 0, len(boundaries))

	for i, b := range boundaries {
		end := len(lines)
		if i+1 < len(boundaries) {
			end = boundaries[i+1].line
		}

		body := strings.TrimSpace(strings.Join(lines[b.line:end], "\n"))
		if body == "" {
			continue
		}

		allSections = append(allSections, section{Label: b.label, Body: body})
	}

	// Many files in the sample corpus open with metadata lines (Severity,
	// Affected file, Risk, ...) immediately after the title, before any free
	// text, and put the real prose in an explicitly labeled "Description"
	// section further down. Prefer that section when present so the
	// metadata lines don't leave the description empty.
	descIdx := -1

	for i, s := range allSections {
		if strings.EqualFold(s.Label, "description") {
			descIdx = i

			break
		}
	}

	switch {
	case descIdx >= 0:
		description = allSections[descIdx].Body
		comments = append(comments, allSections[:descIdx]...)
		comments = append(comments, allSections[descIdx+1:]...)
	case preText != "":
		description = preText
		comments = allSections
	case len(allSections) > 0:
		// No free-standing intro and no explicit Description section: fall
		// back to the first section so the description is never empty.
		description = allSections[0].Body
		comments = allSections[1:]
	}

	return description, comments
}

// boundaryLabel checks a single trimmed line against the three recognized
// section-boundary conventions and returns the label text if one matches.
func boundaryLabel(trimmed string) (string, bool) {
	if m := headerPattern.FindStringSubmatch(trimmed); m != nil {
		return strings.TrimSpace(m[1]), true
	}

	if m := boldLabelPattern.FindStringSubmatch(trimmed); m != nil {
		return strings.TrimSpace(m[1]), true
	}

	if m := plainLabelPattern.FindStringSubmatch(trimmed); m != nil {
		return strings.TrimSpace(m[1]), true
	}

	return "", false
}

// firstLineOf returns the first line of s.
func firstLineOf(s string) string {
	if idx := strings.IndexByte(s, '\n'); idx >= 0 {
		return s[:idx]
	}

	return s
}

// detectStatus implements rule 5: look for explicit resolution/status
// markers among the section labels first (most reliable), then fall back to
// a narrower whole-text scan requiring the word "status" near a
// resolved/fixed/closed word on the same line. matched is false when no
// signal was found, telling the caller to fall through to --default-status.
func detectStatus(comments []section, fullText string) (status string, matched bool) {
	for _, c := range comments {
		label := strings.ToLower(c.Label)

		if strings.Contains(label, "resolution") {
			return statusResolved, true
		}

		if strings.Contains(label, "status") && resolvedWordPattern.MatchString(firstLineOf(c.Body)) {
			return "Resolved", true
		}
	}

	if statusMarkerPattern.MatchString(fullText) {
		return "Resolved", true
	}

	return "", false
}

// detectPriority scans "Severity"/"Risk" labeled sections for an explicit
// High/Medium/Critical/Low/Minor word, implementing the priority-inference
// extension agreed with the user alongside status detection.
func detectPriority(comments []section) (priority string, matched bool) {
	for _, c := range comments {
		label := strings.ToLower(c.Label)
		if !strings.Contains(label, "severity") && !strings.Contains(label, "risk") {
			continue
		}

		if p, ok := priorityFromText(firstLineOf(c.Body)); ok {
			return p, true
		}
	}

	return "", false
}

func priorityFromText(s string) (string, bool) {
	switch {
	case highPriorityPattern.MatchString(s):
		return priorityHigh, true
	case mediumPriorityPattern.MatchString(s):
		return priorityMedium, true
	case lowPriorityPattern.MatchString(s):
		return priorityLow, true
	}

	return "", false
}

// Scoring weights for project/component inference. Filename tokens are the
// strongest signal (the corpus's naming convention, e.g. "OAUTH-H3.md",
// directly encodes the topic), title tokens are next, and body occurrences
// (capped, to stop one very repetitive file from dominating) are the
// weakest.
const (
	filenameWeight    = 6
	titleWeight       = 3
	bodyWeight        = 1
	bodyOccurrenceCap = 3
	minInferScore     = 6
)

// inferenceResult carries both the resolved project/component and where each
// came from, so --test mode can report a confidence source per file.
type inferenceResult struct {
	Project         string
	ProjectSource   string // "inferred" or "default"
	ProjectScore    int
	Component       string
	ComponentSource string // "inferred" or "default"
	ComponentScore  int
}

// tokenize lower-cases s and splits it into alphanumeric words of at least 3
// characters, discarding punctuation and short/noisy tokens.
func tokenize(s string) []string {
	parts := nonAlnumPattern.Split(strings.ToLower(s), -1)

	out := make([]string, 0, len(parts))

	for _, p := range parts {
		if len(p) >= 3 {
			out = append(out, p)
		}
	}

	return out
}

// tokenizeFilename tokenizes a file's base name and drops severity-code
// tokens like "h3"/"m1"/"l2" (from names such as "OAUTH-H3.md") that encode
// severity, not topic, and would otherwise be noise for project/component
// matching.
func tokenizeFilename(filename string) []string {
	base := filepath.Base(filename)
	base = strings.TrimSuffix(base, filepath.Ext(base))

	tokens := tokenize(base)
	out := tokens[:0]

	for _, t := range tokens {
		if !severityCodeToken.MatchString(t) {
			out = append(out, t)
		}
	}

	return out
}

func containsToken(tokens []string, t string) bool {
	for _, tok := range tokens {
		if tok == t {
			return true
		}
	}

	return false
}

// wordCount counts whole-word, case-insensitive occurrences of token in text
// (text is expected to already be lower-cased; token is always lower-cased by
// tokenize).
func wordCount(text, token string) int {
	re := regexp.MustCompile(`\b` + regexp.QuoteMeta(token) + `\b`)

	return len(re.FindAllStringIndex(text, -1))
}

// scoreName computes a weighted match score for a single project or
// component name against the filename tokens, title tokens, and full body
// text of a candidate file.
func scoreName(name string, filenameTokens, titleTokens []string, bodyLower string) int {
	nameTokens := tokenize(name)
	if len(nameTokens) == 0 {
		return 0
	}

	score := 0

	for _, nt := range nameTokens {
		if containsToken(filenameTokens, nt) {
			score += filenameWeight
		}

		if containsToken(titleTokens, nt) {
			score += titleWeight
		}

		score += min(wordCount(bodyLower, nt), bodyOccurrenceCap) * bodyWeight
	}

	return score
}

func containsComponentCI(components []string, name string) bool {
	for _, c := range components {
		if strings.EqualFold(c, name) {
			return true
		}
	}

	return false
}

// inferProjectComponent implements rule 3: weighted keyword scoring of every
// known (project, component) pair against the file's name, title, and body,
// falling back to the operator-supplied defaults when no pair scores above
// minInferScore. Project and component are allowed to fall back
// independently: an inferred project whose evidence doesn't extend to any of
// its own components still keeps the inferred project, using the
// default-component only if it is valid under that project; otherwise the
// whole pair reverts to the defaults so an inferred project is never mixed
// with a component from an unrelated project.
func inferProjectComponent(filename, title, description string, comments []section, projects []db.Project, defaultProject, defaultComponent string) inferenceResult {
	var body strings.Builder

	body.WriteString(description)

	for _, c := range comments {
		body.WriteString("\n")
		body.WriteString(c.Body)
	}

	bodyLower := strings.ToLower(body.String())
	filenameTokens := tokenizeFilename(filename)
	titleTokens := tokenize(title)

	var (
		bestProject      db.Project
		bestProjectScore int
		bestPairProject  db.Project
		bestPairComp     string
		bestPairScore    int
		bestPairCompOnly int
	)

	for _, p := range projects {
		pScore := scoreName(p.Name, filenameTokens, titleTokens, bodyLower)
		if pScore > bestProjectScore {
			bestProject, bestProjectScore = p, pScore
		}

		for _, comp := range p.Components {
			cScore := scoreName(comp, filenameTokens, titleTokens, bodyLower)
			total := pScore + cScore

			if total > bestPairScore {
				bestPairProject, bestPairComp, bestPairScore, bestPairCompOnly = p, comp, total, cScore
			}
		}
	}

	result := inferenceResult{
		Project:         defaultProject,
		ProjectSource:   useDefault,
		Component:       defaultComponent,
		ComponentSource: useDefault,
	}

	if bestPairScore >= minInferScore && bestPairProject.Name != "" && bestPairCompOnly > 0 {
		// Report the combined pair score for both fields rather than
		// splitting it: a pair can clear the threshold on component
		// evidence alone (a component belongs to exactly one project, so
		// strong component evidence is itself valid project evidence too),
		// and showing a per-field split could misleadingly read as
		// "inferred:0" when the project name itself had no direct hits.
		result.Project = bestPairProject.Name
		result.ProjectSource = useInferred
		result.ProjectScore = bestPairScore
		result.Component = bestPairComp
		result.ComponentSource = useInferred
		result.ComponentScore = bestPairScore

		return result
	}

	if bestProjectScore >= minInferScore && bestProject.Name != "" {
		result.ProjectScore = bestProjectScore

		if containsComponentCI(bestProject.Components, defaultComponent) {
			result.Project = bestProject.Name
			result.ProjectSource = useInferred
		}
	}

	return result
}
