package commands

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tucats/idtrack/db"
)

// Define handles the "define" sub-command. The first positional argument is a
// subcommand: "project" (creates a new project) or "component" (adds a
// component to an existing project). Both operations are idempotent.
func Define(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "define requires a subcommand: project or component")
		Usage()
		os.Exit(1)
	}

	subcommand := args[0]
	rest := args[1:]

	var project, component, database string

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
		if err := db.CreateProject(d, project); err != nil {
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

// Delete handles the "delete" sub-command. The first positional argument is a
// subcommand: "project" (removes the whole project and all its components) or
// "component" (removes one component). Both refuse to delete if any issues
// reference the target, returning the blocking issue IDs.
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
