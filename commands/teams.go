package commands

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tucats/idtrack/db"
)

// Teams handles the "teams" sub-command.
// Subcommands: list, add, delete, update.
func Teams(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "teams requires a subcommand: list, add, delete, or update")
		Usage()
		os.Exit(1)
	}

	subcommand := args[0]
	rest := args[1:]

	var (
		name        string
		newName     string
		description string
		database    string
	)

	switch subcommand {
	case listCommand:
		// no positional args required

	case addCommand:
		if len(rest) == 0 || strings.HasPrefix(rest[0], "--") {
			fmt.Fprintln(os.Stderr, "teams add requires a team name")
			Usage()
			os.Exit(1)
		}

		name = rest[0]
		rest = rest[1:]

	case deleteCommand:
		if len(rest) == 0 || strings.HasPrefix(rest[0], "--") {
			fmt.Fprintln(os.Stderr, "teams delete requires a team name")
			Usage()
			os.Exit(1)
		}

		name = rest[0]
		rest = rest[1:]

	case updateCommand:
		if len(rest) == 0 || strings.HasPrefix(rest[0], "--") {
			fmt.Fprintln(os.Stderr, "teams update requires a team name")
			Usage()
			os.Exit(1)
		}

		name = rest[0]
		rest = rest[1:]

	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n", subcommand)
		Usage()
		os.Exit(1)
	}

	for i := 0; i < len(rest); i++ {
		switch rest[i] {
		case "--name", "-n":
			if i+1 < len(rest) {
				i++
				newName = rest[i]
			}

		case "--description", "--desc":
			if i+1 < len(rest) {
				i++
				description = rest[i]
			}

		case databaseFlag, "-d":
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

	switch subcommand {
	case listCommand:
		teams, err := db.ListTeams(d)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error listing teams: %v\n", err)
			os.Exit(1)
		}

		fmt.Printf("%-20s  %s\n", "NAME", "DESCRIPTION")
		fmt.Printf("%-20s  %s\n", strings.Repeat("-", 20), strings.Repeat("-", 40))

		for _, t := range teams {
			fmt.Printf("%-20s  %s\n", t.Name, t.Description)
		}

	case addCommand:
		if err := db.CreateTeam(d, name, description); err != nil {
			fmt.Fprintf(os.Stderr, "error creating team %q: %v\n", name, err)
			os.Exit(1)
		}

		fmt.Printf("team %q created\n", name)

	case deleteCommand:
		if err := db.DeleteTeam(d, name); err != nil {
			fmt.Fprintf(os.Stderr, "error deleting team %q: %v\n", name, err)
			os.Exit(1)
		}

		fmt.Printf("team %q deleted\n", name)

	case updateCommand:
		if newName == "" && description == "" {
			fmt.Fprintln(os.Stderr, "teams update requires at least --name or --description")
			Usage()
			os.Exit(1)
		}

		if err := db.UpdateTeam(d, name, newName, description); err != nil {
			fmt.Fprintf(os.Stderr, "error updating team %q: %v\n", name, err)
			os.Exit(1)
		}

		fmt.Printf("team %q updated\n", name)
	}
}
