package commands

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/tucats/idtrack/db"
)

const (
	statusOpen     = "Open"
	statusResolved = "Resolved"
	priorityLow    = "Low"
	priorityMedium = "Medium"
	priorityHigh   = "High"
	useDefault     = "default"
	useInferred    = "inferred"
	useDetected    = "detected"
)

// ingestPlan is the fully-resolved, DB-independent result of parsing one
// ingest input file. Plans are built for every file before any database
// write happens, so a parse/validation failure in any one file aborts the
// whole batch without touching the database (the atomicity requirement).
type ingestPlan struct {
	Path   string
	Title  string
	Format string

	Description string
	Comments    []section

	Status       string
	StatusSource string // "detected" or "default"

	Priority       string
	PrioritySource string // "detected" or "default"

	Project         string
	ProjectSource   string // "inferred" or "default"
	ProjectScore    int
	Component       string
	ComponentSource string // "inferred" or "default"
	ComponentScore  int
}

// Ingest handles the "ingest" sub-command: bulk-creates one issue per input
// file. See resources/MANUAL.md for the full flag reference.
func Ingest(args []string) {
	var (
		author, defaultOwner, defaultProjectFlag, defaultComponentFlag string
		defaultStatusFlag                                              = "open"
		defaultPriorityFlag                                            = priorityMedium
		database                                                       string
		test                                                           bool
		files                                                          []string
	)

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--author":
			if i+1 < len(args) {
				i++
				author = args[i]
			}

		case "--default-owner":
			if i+1 < len(args) {
				i++
				defaultOwner = args[i]
			}

		case "--default-project":
			if i+1 < len(args) {
				i++
				defaultProjectFlag = args[i]
			}

		case "--default-component":
			if i+1 < len(args) {
				i++
				defaultComponentFlag = args[i]
			}

		case "--default-status":
			if i+1 < len(args) {
				i++
				defaultStatusFlag = args[i]
			}

		case "--default-priority":
			if i+1 < len(args) {
				i++
				defaultPriorityFlag = args[i]
			}

		case "--test":
			test = true

		case databaseFlag:
			if i+1 < len(args) {
				i++
				database = args[i]
			}

		default:
			if strings.HasPrefix(args[i], "--") {
				fmt.Fprintf(os.Stderr, "unknown option: %s\n", args[i])
				Usage()
				os.Exit(1)
			}

			files = append(files, args[i])
		}
	}

	var missing []string

	for _, req := range []struct{ name, value string }{
		{"--author", author},
		{"--default-owner", defaultOwner},
		{"--default-project", defaultProjectFlag},
		{"--default-component", defaultComponentFlag},
	} {
		if req.value == "" {
			missing = append(missing, req.name)
		}
	}

	if len(missing) > 0 {
		fmt.Fprintf(os.Stderr, "ingest requires %s\n", strings.Join(missing, ", "))
		Usage()
		os.Exit(1)
	}

	if len(files) == 0 {
		fmt.Fprintln(os.Stderr, "ingest requires at least one file")
		Usage()
		os.Exit(1)
	}

	defaultStatus, err := normalizeStatusFlag(defaultStatusFlag)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	defaultPriority, err := normalizePriorityFlag(defaultPriorityFlag)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	if database == "" {
		database = loadDefaults().Database
	}

	if database == "" {
		database = defaultDB
	}

	if abs, err := filepath.Abs(database); err == nil {
		database = abs
	}

	d, err := db.Open(database)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error opening database %q: %v\n", database, err)
		os.Exit(1)
	}

	defer d.Close()

	authorUser, err := db.FindUser(d, author)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error looking up user %q: %v\n", author, err)
		os.Exit(1)
	}

	if authorUser == nil {
		fmt.Fprintf(os.Stderr, "--author %q is not a known user\n", author)
		os.Exit(1)
	}

	ownerUser, err := db.FindUser(d, defaultOwner)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error looking up user %q: %v\n", defaultOwner, err)
		os.Exit(1)
	}

	if ownerUser == nil {
		fmt.Fprintf(os.Stderr, "--default-owner %q is not a known user\n", defaultOwner)
		os.Exit(1)
	}

	projects, err := db.ListProjects(d)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error listing projects: %v\n", err)
		os.Exit(1)
	}

	defaultProject, ok := findProject(projects, defaultProjectFlag)
	if !ok {
		fmt.Fprintf(os.Stderr, "--default-project %q does not exist\n", defaultProjectFlag)
		os.Exit(1)
	}

	if !containsComponentCI(defaultProject.Components, defaultComponentFlag) {
		fmt.Fprintf(os.Stderr, "--default-component %q does not exist in project %q\n", defaultComponentFlag, defaultProject.Name)
		os.Exit(1)
	}

	// Phase 1: parse and validate every file without touching the database.
	// Any failure here aborts the whole batch before any write is attempted.
	plans, parseFails := parseIngestFiles(files, projects, defaultProject.Name, defaultComponentFlag, defaultStatus, defaultPriority)

	if len(parseFails) > 0 {
		fmt.Fprintln(os.Stderr, "ingest aborted, no changes made — the following files failed to parse:")

		for _, f := range parseFails {
			fmt.Fprintf(os.Stderr, "  %s\n", f)
		}

		os.Exit(1)
	}

	if test {
		printIngestReport(plans)

		return
	}

	// Phase 2: everything validated — create all issues inside a single
	// transaction so a failure partway through leaves the database
	// unchanged.
	n, err := runIngestTx(d, plans, authorUser.Username, ownerUser.Username)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v — no changes made\n", err)
		os.Exit(1)
	}

	fmt.Printf("ingested %d issue(s) from %d file(s)\n", n, len(files))
}

// parseIngestFiles runs buildIngestPlan over every file, returning the
// successfully-parsed plans and a human-readable failure message per file
// that could not be parsed. It never touches the database.
func parseIngestFiles(files []string, projects []db.Project, defaultProject, defaultComponent, defaultStatus, defaultPriority string) (plans []ingestPlan, failures []string) {
	for _, path := range files {
		plan, err := buildIngestPlan(path, projects, defaultProject, defaultComponent, defaultStatus, defaultPriority)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", path, err))

			continue
		}

		plans = append(plans, plan)
	}

	return plans, failures
}

// runIngestTx creates one issue (plus its comments, plus a Resolved
// transition where applicable) per plan, inside a single transaction, so
// that any failure partway through leaves the database completely
// unchanged. It returns the number of issues created on success.
func runIngestTx(d *sql.DB, plans []ingestPlan, reporterUsername, assigneeUsername string) (int, error) {
	tx, err := d.Begin()
	if err != nil {
		return 0, fmt.Errorf("starting transaction: %w", err)
	}

	for _, plan := range plans {
		issue, err := db.CreateIssue(tx, plan.Title, plan.Description, reporterUsername, assigneeUsername, plan.Priority, plan.Project, plan.Component, plan.Format)
		if err != nil {
			tx.Rollback() //nolint:errcheck

			return 0, fmt.Errorf("creating issue from %q: %w", plan.Path, err)
		}

		for _, c := range plan.Comments {
			if _, err := db.CreateComment(tx, issue.ID, reporterUsername, c.Body); err != nil {
				tx.Rollback() //nolint:errcheck

				return 0, fmt.Errorf("creating comment from %q: %w", plan.Path, err)
			}
		}

		if plan.Status == statusResolved {
			if _, err := db.UpdateIssue(tx, issue.ID, issue.Title, issue.Description, issue.Priority, statusResolved, issue.Assignee, issue.Project, issue.Component, issue.Format, nil, nil); err != nil {
				tx.Rollback() //nolint:errcheck

				return 0, fmt.Errorf("resolving issue from %q: %w", plan.Path, err)
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("committing ingest: %w", err)
	}

	return len(plans), nil
}

// buildIngestPlan reads and parses a single file into an ingestPlan. It does
// not touch the database.
func buildIngestPlan(path string, projects []db.Project, defaultProject, defaultComponent, defaultStatus, defaultPriority string) (ingestPlan, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return ingestPlan{}, fmt.Errorf("reading file: %w", err)
	}

	content := string(data)

	format := "text"
	if strings.EqualFold(filepath.Ext(path), ".md") {
		format = "markdown"
	}

	title := extractTitle(content)
	if title == "" {
		return ingestPlan{}, fmt.Errorf("could not derive a title")
	}

	description, comments := splitSections(content)
	if description == "" {
		return ingestPlan{}, fmt.Errorf("no description content found")
	}

	status, statusMatched := detectStatus(comments, content)
	if !statusMatched {
		status = defaultStatus
	}

	priority, priorityMatched := detectPriority(comments)
	if !priorityMatched {
		priority = defaultPriority
	}

	inference := inferProjectComponent(path, title, description, comments, projects, defaultProject, defaultComponent)

	return ingestPlan{
		Path:            path,
		Title:           title,
		Format:          format,
		Description:     description,
		Comments:        comments,
		Status:          status,
		StatusSource:    sourceLabel(statusMatched),
		Priority:        priority,
		PrioritySource:  sourceLabel(priorityMatched),
		Project:         inference.Project,
		ProjectSource:   inference.ProjectSource,
		ProjectScore:    inference.ProjectScore,
		Component:       inference.Component,
		ComponentSource: inference.ComponentSource,
		ComponentScore:  inference.ComponentScore,
	}, nil
}

func sourceLabel(matched bool) string {
	if matched {
		return useDetected
	}

	return useDefault
}

func normalizeStatusFlag(s string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "open":
		return statusOpen, nil
	case "resolved":
		return statusResolved, nil
	default:
		return "", fmt.Errorf("--default-status must be 'open' or 'resolved', got %q", s)
	}
}

func normalizePriorityFlag(s string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "high":
		return priorityHigh, nil
	case "medium":
		return priorityMedium, nil
	case "low":
		return priorityLow, nil
	default:
		return "", fmt.Errorf("--default-priority must be 'High', 'Medium', or 'Low', got %q", s)
	}
}

func findProject(projects []db.Project, name string) (db.Project, bool) {
	for _, p := range projects {
		if strings.EqualFold(p.Name, name) {
			return p, true
		}
	}

	return db.Project{}, false
}

// printIngestReport implements --test mode: a summary table showing what
// would be created, with a source annotation per inferred/defaulted field so
// the scoring thresholds can be validated against a real corpus before doing
// a live ingest.
func printIngestReport(plans []ingestPlan) {
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)

	fmt.Fprintln(w, "FILE\tTITLE\tPROJECT\tCOMPONENT\tSTATUS\tPRIORITY\tCOMMENTS")

	for _, p := range plans {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%d\n",
			filepath.Base(p.Path),
			truncate(p.Title, 60),
			tagValue(p.Project, p.ProjectSource, p.ProjectScore),
			tagValue(p.Component, p.ComponentSource, p.ComponentScore),
			p.Status+" ("+p.StatusSource+")",
			p.Priority+" ("+p.PrioritySource+")",
			len(p.Comments),
		)
	}

	w.Flush() //nolint:errcheck

	fmt.Printf("\n%d issue(s) would be created (--test mode, no changes made)\n", len(plans))
}

func tagValue(value, source string, score int) string {
	if source == useInferred {
		return fmt.Sprintf("%s (inferred:%d)", value, score)
	}

	return value + " (default)"
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}

	return s[:n-1] + "…"
}
