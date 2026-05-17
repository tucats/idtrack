package commands

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/tucats/idtrack/db"
)

// User handles the "user" sub-command. A single invocation may perform only
// one of list, add, update, or delete. Flags are parsed first, validated, and
// only then is the database opened — this avoids creating the DB file for a
// bad invocation.
func User(args []string) {
	var (
		add, del, update, name, password, database, adminStr string
		list                                                  bool
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

				// Normalize to "true"/"false" to avoid ambiguity in UpdateUser's
				// *bool parameter, and reject anything that isn't a valid bool.
				if value, err := strconv.ParseBool(adminStr); err != nil {
					fmt.Fprintln(os.Stderr, "--admin requires true or false")
					os.Exit(1)
				} else {
					adminStr = strconv.FormatBool(value)
				}
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

		fmt.Printf("%-20s  %-30s  %-7s  %s\n", "USERNAME", "DISPLAY NAME", "ADMIN", "LAST LOGIN")
		fmt.Printf("%-20s  %-30s  %-7s  %s\n",
			strings.Repeat("-", 20), strings.Repeat("-", 30),
			strings.Repeat("-", 7), strings.Repeat("-", 25))

		for _, u := range users {
			lastLogin := u.LastLoginAt
			if lastLogin == "" {
				lastLogin = "(never)"
			}

			admin := "no"
			if u.IsAdmin {
				admin = "yes"
			}

			fmt.Printf("%-20s  %-30s  %-7s  %s\n", u.Username, u.DisplayName, admin, lastLogin)
		}
	}

	if add != "" {
		// The add value must be "username:password". SplitN with n=2 ensures
		// that a password containing ":" is not split further.
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

		if err := db.AddUser(d, username, displayName, pwd, adminStr == trueValue); err != nil {
			fmt.Fprintf(os.Stderr, "error adding user %q: %v\n", username, err)
			os.Exit(1)
		}

		fmt.Printf("user %q added\n", username)
	}

	if update != "" {
		if name == "" && password == "" && adminStr == "" {
			fmt.Fprintln(os.Stderr, "update requires at least --name, --password, or --admin")
			Usage()
			os.Exit(1)
		}

		// db.UpdateUser uses *bool for the admin flag so that nil means
		// "not specified" — a plain bool has no way to represent that.
		var adminPtr *bool

		if adminStr != "" {
			val := adminStr == trueValue
			adminPtr = &val
		}

		if err := db.UpdateUser(d, update, name, password, adminPtr); err != nil {
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
