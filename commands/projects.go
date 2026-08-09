package commands

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tucats/idtrack/db"
)

// Define handles the "define" sub-command.
//
//	idtrack define project <name> [--database path] [--teams a,b,...]
//	idtrack define component <project-name> <component-name> [--database path]
//
// The first positional argument (args[0]) is itself a subcommand word —
// "project" or "component" — not a flag. This "positional subcommand"
// pattern (verb, then its own required positional values, then optional
// named flags) is used consistently across this package: see also Delete
// below, and User in users.go and Teams in teams.go. It reads naturally as
// an English sentence ("define project foo") and keeps the verb from being
// confused with an option, at the cost of not being parseable by the
// standard flag.FlagSet (which expects flags before positional arguments,
// not a verb of their own).
//
// Both "project" and "component" creation are idempotent — running the same
// command twice is not an error, it just leaves the row already present.
// On success it prints a one-line confirmation; on any validation or
// database error it prints to stderr and exits with status 1.
func Define(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "define requires a subcommand: project or component")
		Usage()
		os.Exit(1)
	}

	subcommand := args[0]
	rest := args[1:] // rest holds the subcommand's own args, with args[0] consumed

	var project, component, database, teamsStr string

	switch subcommand {
	case "project":
		if len(rest) == 0 || strings.HasPrefix(rest[0], "--") {
			fmt.Fprintln(os.Stderr, "define project requires a project name")
			Usage()
			os.Exit(1)
		}

		project = rest[0]
		rest = rest[1:]

	case "component":
		if len(rest) < 2 || strings.HasPrefix(rest[0], "--") || strings.HasPrefix(rest[1], "--") {
			fmt.Fprintln(os.Stderr, "define component requires a project name and a component name")
			Usage()
			os.Exit(1)
		}

		project = rest[0]
		component = rest[1]
		rest = rest[2:]

	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n", subcommand)
		Usage()
		os.Exit(1)
	}

	for i := 0; i < len(rest); i++ {
		switch rest[i] {
		case databaseFlag:
			if i+1 < len(rest) {
				i++
				database = rest[i]
			}

		case "--teams", "-t":
			if i+1 < len(rest) {
				i++
				teamsStr = rest[i]
			}

		default:
			fmt.Fprintf(os.Stderr, "unknown option: %s\n", rest[i])
			Usage()
			os.Exit(1)
		}
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

	if component == "" {
		var teams []string
		if teamsStr != "" {
			teams = db.ParseTeams(teamsStr)
		}

		if err := db.CreateProject(d, project, teams); err != nil {
			fmt.Fprintf(os.Stderr, "error creating project %q: %v\n", project, err)
			os.Exit(1)
		}

		fmt.Printf("project %q defined\n", project)
	} else {
		if err := db.AddComponent(d, project, component); err != nil {
			fmt.Fprintf(os.Stderr, "error adding component %q to project %q: %v\n", component, project, err)
			os.Exit(1)
		}

		fmt.Printf("component %q added to project %q\n", component, project)
	}
}

// Delete handles the "delete" sub-command.
//
//	idtrack delete project <name> [--database path]
//	idtrack delete component <project-name> <component-name> [--database path]
//
// Same positional-subcommand shape as Define above: args[0] is "project" or
// "component". Both operations refuse to delete their target if any issue
// still references it — the underlying db.DeleteProject/db.DeleteComponent
// calls return an error listing the blocking issue IDs, which is printed to
// stderr verbatim before exiting with status 1. This is a safety check, not
// a cascading delete: issues are never modified or removed by this command.
func Delete(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "delete requires a subcommand: project or component")
		Usage()
		os.Exit(1)
	}

	subcommand := args[0]
	rest := args[1:]

	var project, component, database string

	switch subcommand {
	case "project":
		if len(rest) == 0 || strings.HasPrefix(rest[0], "--") {
			fmt.Fprintln(os.Stderr, "delete project requires a project name")
			Usage()
			os.Exit(1)
		}

		project = rest[0]
		rest = rest[1:]

	case "component":
		if len(rest) < 2 || strings.HasPrefix(rest[0], "--") || strings.HasPrefix(rest[1], "--") {
			fmt.Fprintln(os.Stderr, "delete component requires a project name and a component name")
			Usage()
			os.Exit(1)
		}

		project = rest[0]
		component = rest[1]
		rest = rest[2:]

	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n", subcommand)
		Usage()
		os.Exit(1)
	}

	for i := 0; i < len(rest); i++ {
		switch rest[i] {
		case databaseFlag:
			if i+1 < len(rest) {
				i++
				database = rest[i]
			}

		default:
			fmt.Fprintf(os.Stderr, "unknown option: %s\n", rest[i])
			Usage()
			os.Exit(1)
		}
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

	if component == "" {
		if err := db.DeleteProject(d, project); err != nil {
			fmt.Fprintf(os.Stderr, "error deleting project %q: %v\n", project, err)
			os.Exit(1)
		}

		fmt.Printf("project %q deleted\n", project)
	} else {
		if err := db.DeleteComponent(d, project, component); err != nil {
			fmt.Fprintf(os.Stderr, "error deleting component %q from project %q: %v\n", component, project, err)
			os.Exit(1)
		}

		fmt.Printf("component %q deleted from project %q\n", component, project)
	}
}
