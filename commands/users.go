package commands

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/tucats/idtrack/db"
)

// User handles the "user" (alias: "users") sub-command, which manages login
// accounts.
//
//	idtrack user list
//	idtrack user add username:password [--name text] [--admin true|false] [--teams a,b,...] [--database path]
//	idtrack user update username [--name text] [--password text] [--admin true|false] [--teams a,b,...] [--database path]
//	idtrack user delete username [--database path]
//
// Unlike Define/Delete/Teams, "list"/"add"/"delete"/"update" are recognized
// here as flags rather than args[0] — but they still behave like positional
// subcommands in spirit: exactly one of them must be supplied (checked
// after the parsing loop below), and it selects which action runs.
//
//   - list prints a USERNAME/DISPLAY NAME/TEAMS/LAST LOGIN table.
//   - add takes its target as "username:password" (colon-separated, both
//     parts required) rather than two separate flags. Display name defaults
//     to the username. --admin is a convenience alias for team membership:
//     "--admin true" adds the reserved "admin" team; when neither --teams
//     nor --admin is given, the new user defaults to the reserved "any" team
//     (see buildTeamsFromFlags). Upserts silently if the username already
//     exists.
//   - update requires at least one of --name, --password, --admin, or
//     --teams (erroring otherwise) and only changes the fields explicitly
//     provided — see the *bool discussion below for how "--admin" not being
//     passed at all is distinguished from "--admin false".
//   - delete hard-deletes the row. It does not cascade to issues/comments;
//     a deleted user's name simply remains as a dangling reporter/assignee
//     string on any issues they touched (see CLAUDE.md's note on this).
//
// Every path prints a one-line confirmation on success, or an error to
// stderr followed by os.Exit(1) on failure (including a database-enforced
// "last admin" guard on delete/update — see db.CountAdmins).
func User(args []string) {
	var (
		add, del, update, name, password, database, adminStr, teamsStr string
		list                                                           bool
	)

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "list":
			list = true

		case "add":
			if i+1 < len(args) {
				i++
				add = args[i]
			}

		case "delete":
			if i+1 < len(args) {
				i++
				del = args[i]
			}

		case "update":
			if i+1 < len(args) {
				i++
				update = args[i]
			}

		case "--name", "-n":
			if i+1 < len(args) {
				i++
				name = args[i]
			}

		case "--password", "-p":
			if i+1 < len(args) {
				i++
				password = args[i]
			}

		case "--admin", "-a":
			if i+1 < len(args) {
				i++
				adminStr = args[i]

				if value, err := strconv.ParseBool(adminStr); err != nil {
					fmt.Fprintln(os.Stderr, "--admin requires true or false")
					os.Exit(1)
				} else {
					adminStr = strconv.FormatBool(value)
				}
			}

		case "--teams", "-t":
			if i+1 < len(args) {
				i++
				teamsStr = args[i]
			}

		case databaseFlag, "-d":
			if i+1 < len(args) {
				i++
				database = args[i]
			}

		default:
			fmt.Fprintf(os.Stderr, "unknown option: %s\n", args[i])
			Usage()
			os.Exit(1)
		}
	}

	if !list && add == "" && del == "" && update == "" {
		fmt.Fprintln(os.Stderr, "must specify list, add, update, or delete")
		Usage()
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

	if list {
		users, err := db.ListUsers(d)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error listing users: %v\n", err)
			os.Exit(1)
		}

		fmt.Printf("%-20s  %-30s  %-20s  %s\n", "USERNAME", "DISPLAY NAME", "TEAMS", "LAST LOGIN")
		fmt.Printf("%-20s  %-30s  %-20s  %s\n",
			strings.Repeat("-", 20), strings.Repeat("-", 30),
			strings.Repeat("-", 20), strings.Repeat("-", 25))

		for _, u := range users {
			lastLogin := u.LastLoginAt
			if lastLogin == "" {
				lastLogin = "(never)"
			}

			fmt.Printf("%-20s  %-30s  %-20s  %s\n",
				u.Username, u.DisplayName,
				strings.Join(u.Teams, ","), lastLogin)
		}
	}

	if add != "" {
		parts := strings.SplitN(add, ":", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			fmt.Fprintln(os.Stderr, "add requires username:password")
			os.Exit(1)
		}

		username, pwd := parts[0], parts[1]

		displayName := username
		if name != "" {
			displayName = name
		}

		teams := buildTeamsFromFlags(teamsStr, adminStr)

		if err := db.AddUser(d, username, displayName, pwd, teams); err != nil {
			fmt.Fprintf(os.Stderr, "error adding user %q: %v\n", username, err)
			os.Exit(1)
		}

		fmt.Printf("user %q added\n", username)
	}

	if update != "" {
		if name == "" && password == "" && adminStr == "" && teamsStr == "" {
			fmt.Fprintln(os.Stderr, "update requires at least --name, --password, --admin, or --teams")
			Usage()
			os.Exit(1)
		}

		// db.UpdateUser takes *bool (a pointer to a bool) rather than plain
		// bool for the admin flag. A plain bool can only ever be true or
		// false — there's no third value it can hold to mean "the caller
		// didn't say anything about this field, leave it as-is". A pointer
		// gives us that third state for free: nil means "not specified,
		// don't touch it", while a non-nil pointer (even one pointing at
		// false) means "set it to exactly this value". This is the standard
		// Go idiom for an optional/tri-state field of a type that has no
		// natural empty value to serve as a sentinel — contrast with the
		// string fields below (name, password), where the empty string
		// "" already means "not specified" without needing a pointer.
		var adminPtr *bool

		if adminStr != "" {
			val := adminStr == trueValue
			adminPtr = &val
		}

		// Parse teams if provided; nil means "no change".
		var teamsList []string
		if teamsStr != "" {
			teamsList = parseTeamsFlag(teamsStr)
		}

		if err := db.UpdateUser(d, update, name, password, adminPtr, teamsList); err != nil {
			fmt.Fprintf(os.Stderr, "error updating user %q: %v\n", update, err)
			os.Exit(1)
		}

		fmt.Printf("user %q updated\n", update)
	}

	if del != "" {
		if err := db.DeleteUser(d, del); err != nil {
			fmt.Fprintf(os.Stderr, "error deleting user %q: %v\n", del, err)
			os.Exit(1)
		}

		fmt.Printf("user %q deleted\n", del)
	}
}

// parseTeamsFlag splits a comma-separated teams string into a []string.
func parseTeamsFlag(s string) []string {
	return db.ParseTeams(s)
}

// buildTeamsFromFlags merges --teams and --admin into a single team list.
// When neither is given, defaults to ["any"].
func buildTeamsFromFlags(teamsStr, adminStr string) []string {
	var teams []string

	if teamsStr != "" {
		teams = parseTeamsFlag(teamsStr)
	}

	if adminStr == trueValue {
		if !db.ContainsTeam(teams, db.TeamAdmin) {
			teams = append(teams, db.TeamAdmin)
		}
	} else if adminStr == "false" && len(teams) == 0 {
		teams = []string{db.TeamAny}
	}

	if len(teams) == 0 {
		teams = []string{db.TeamAny}
	}

	return teams
}
