// Package commands implements each idtrack CLI sub-command as an exported
// function. main.go sets BuildVersion and BuildTime from the link-time
// injected variables, then dispatches to these functions based on os.Args.
package commands

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	databaseFlag = "--database"
	defaultDB    = "idtrack.db"
	trueValue    = "true"
)

// BuildVersion and BuildTime are set by main.go from the link-time injected
// variables before any command function is called.
var (
	BuildVersion string
	BuildTime    string
)

// defaults holds the persisted user preferences stored in
// ~/.idtrack/defaults.json. Fields tagged omitempty are omitted from the file
// when they hold their zero value so the JSON stays minimal.
type defaults struct {
	Port           int    `json:"port"`
	Database       string `json:"database"`
	ServerCert     string `json:"server_cert,omitempty"`     // absolute path to TLS cert file; empty = auto-generated self-signed cert
	ServerKey      string `json:"server_key,omitempty"`      // absolute path to TLS key file; empty = auto-generated self-signed key
	IdleTimeout    int    `json:"idle_timeout,omitempty"`    // seconds; 0 means disabled
	AppName        string `json:"app_name,omitempty"`        // custom branding name
	AppDescription string `json:"app_description,omitempty"` // custom branding tagline
	BackupInterval string `json:"backup_interval,omitempty"` // Go duration string; empty = disabled
	BackupCount    int    `json:"backup_count,omitempty"`    // max backups to retain; 0 = no limit
	BackupAge      string `json:"backup_age,omitempty"`      // Go duration string; empty = no limit
}

// loadDefaults reads ~/.idtrack/defaults.json and returns its contents. If the
// file does not exist or cannot be read, a zero-value struct is returned so
// callers can apply their own fallback values.
//
// Migration: if the stored Database path is a non-empty relative path (written
// by a version of idtrack that did not resolve paths on save), it is converted
// to absolute and the file is rewritten immediately.  This is a one-time
// operation; after migration the file always contains an absolute path and this
// branch becomes a no-op on every subsequent read.
func loadDefaults() defaults {
	var d defaults

	home, err := os.UserHomeDir()
	if err != nil {
		return defaults{}
	}

	path := filepath.Join(home, ".idtrack", "defaults.json")

	data, err := os.ReadFile(path)
	if err != nil {
		return defaults{} // file not yet created — silently use zero values
	}

	json.Unmarshal(data, &d) // ignore parse error; zero struct is a safe fallback

	// Migrate a relative Database path to absolute.  Best-effort: if the
	// rewrite fails for any reason we still return the resolved value so this
	// invocation behaves correctly even if the file cannot be updated.
	if d.Database != "" && !filepath.IsAbs(d.Database) {
		if abs, err := filepath.Abs(d.Database); err == nil {
			d.Database = abs
			if migrated, err := json.MarshalIndent(d, "", "  "); err == nil {
				os.WriteFile(path, append(migrated, '\n'), 0600)
			}
		}
	}

	return d
}

// Usage prints a summary of available sub-commands to stderr. Called from
// main.go when no arguments are given or an unknown verb is used, and from
// within individual command functions when argument validation fails.
func Usage() {
	text := `
idtrack is a self-hosted issue tracker for small teams. It provides a web UI
for managing projects, components, and issues.

Usage:

	idtrack [command] [options]

Commands:

	default [options]
		Set default values for options which are used if not overridden.
		With no options, lists the current defaults.
		 --port <n>
		 --database <path>
		 --server-cert <path>
		 --server-key <path>
		 --idle-timeout <duration> | off
		 --app-name <name>
		 --app-description <text>
		 --backup-interval <duration>|off
		 --backup-count <n> | off
		 --backup-age <duration> | off

	define [subcommand] [options]
		Create projects and components.

		project   <name>
		component <project-name> <component-name>

	delete [subcommand] [options]
		Remove projects and components.

		project   <name>
		component <project-name> <component-name>


	serve
		Start the idtrack server. By default it runs in the background and listens
		on port 8443, but you can override these with options on the command.
		 --port <n>
		 --database <path>
		 --server-cert <path> 
		 --server-key <path> 

	stop
		Stop the running idtrack server.

	restart
		Stop the running server and immediately restart it using the same
		command-line arguments it was originally started with. Useful after
		installing a new binary.

	user [subcommand] [options]
		Manage user accounts.

		list
		add    <username:password> [--name "Display Name"] [--admin true|false] [--password <password>]
		update <username>          [--name "Display Name"] [--admin true|false] [--password <password>]
		delete <username>

	version
		Print the idtrack version.

`

	fmt.Fprintf(os.Stderr, "\nidtrack %s\n\n", strings.TrimSpace(BuildVersion))
	fmt.Fprintf(os.Stderr, "%s\n\n", strings.TrimSpace(text))
}
