// Package commands implements each idtrack CLI sub-command as an exported
// function. main.go sets BuildVersion and BuildTime from the link-time
// injected variables, then dispatches to these functions based on os.Args.
package commands

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Shared string constants used across the command-parsing switch statements
// in this package (users.go, teams.go, projects.go, ingest.go, ...). Defining
// them once here means the flag spelling ("--database") and the positional
// subcommand words ("list"/"add"/"delete"/"update") only need to be changed
// in one place, and the compiler catches typos in the switch cases (a typo in
// a bare string literal would just silently fail to match).
const (
	databaseFlag  = "--database"
	defaultDB     = "idtrack.db"
	trueValue     = "true"
	listCommand   = "list"
	addCommand    = "add"
	deleteCommand = "delete"
	updateCommand = "update"
)

// BuildVersion and BuildTime are set by main.go from the link-time injected
// variables before any command function is called.
var (
	BuildVersion string
	BuildTime    string
)

// defaults holds the persisted user preferences stored in
// ~/.idtrack/defaults.json. The text after each field type (e.g.
// `json:"port"`) is a Go "struct tag" — metadata read by reflection at
// runtime, here by encoding/json, to decide the field's key name in the
// JSON file. Fields tagged omitempty are left out of the file entirely when
// they hold their zero value ("", 0, nil, ...), which keeps a fresh
// defaults.json minimal instead of listing every possible setting at its
// zero value. loadDefaults (below) and Default (in defaults.go) are the two
// places that turn this struct into/from the JSON file via
// json.Unmarshal/json.MarshalIndent.
type defaults struct {
	Port             int    `json:"port"`                         // listen port; 0 means "use the built-in default" (8443)
	Database         string `json:"database"`                     // absolute path to the SQLite database file
	ServerCert       string `json:"server_cert,omitempty"`        // absolute path to TLS cert file; empty = auto-generated self-signed cert
	ServerKey        string `json:"server_key,omitempty"`         // absolute path to TLS key file; empty = auto-generated self-signed key
	IdleTimeout      int    `json:"idle_timeout,omitempty"`       // seconds; 0 means disabled
	AppName          string `json:"app_name,omitempty"`           // custom branding name
	AppDescription   string `json:"app_description,omitempty"`    // custom branding tagline
	BackupInterval   string `json:"backup_interval,omitempty"`    // Go duration string; empty = disabled
	BackupCount      int    `json:"backup_count,omitempty"`       // max backups to retain; 0 = no limit
	BackupAge        string `json:"backup_age,omitempty"`         // Go duration string; empty = no limit
	BackupSize       string `json:"backup_size,omitempty"`        // human-readable size string, e.g. "500mb"; empty = disabled
	Insecure         bool   `json:"insecure,omitempty"`           // true = listen with plain HTTP, no TLS (e.g. behind a TLS-terminating reverse proxy)
	BasePath         string `json:"base_path,omitempty"`          // URL prefix the whole app (page, assets, API) is mounted under; empty = mounted at the origin root
	WebAuthn         bool   `json:"webauthn_enabled,omitempty"`   // true = passkey (Touch ID/Face ID/security key) login is turned on; independent of whether RP ID/origin happen to be set below — see CLAUDE.md
	WebAuthnRPID     string `json:"webauthn_rp_id,omitempty"`     // bare domain the browser sees, e.g. "issues.example.com"; required together with WebAuthnRPOrigin when WebAuthn is true
	WebAuthnRPOrigin string `json:"webauthn_rp_origin,omitempty"` // full browser-facing origin, e.g. "https://issues.example.com"; required together with WebAuthnRPID when WebAuthn is true
	ApnsKeyPath      string `json:"apns_key_path,omitempty"`      // absolute path to the APNs .p8 auth key; required together with ApnsKeyID/ApnsTeamID/ApnsTopic for push notifications to be sent — see docs/NOTIFICATIONS.md
	ApnsKeyID        string `json:"apns_key_id,omitempty"`        // APNs auth key ID, from the Apple Developer portal
	ApnsTeamID       string `json:"apns_team_id,omitempty"`       // Apple Developer Team ID
	ApnsTopic        string `json:"apns_topic,omitempty"`         // APNs topic, i.e. the app's bundle id, e.g. "com.tucats.idtrack"
	ApnsSandbox      bool   `json:"apns_sandbox,omitempty"`       // true = talk to APNs' sandbox environment instead of production (see docs/NOTIFICATIONS.md's accepted limitation on mixed dev/TestFlight deployments)
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

// parseBackupSize converts a human-readable size string to bytes.
// Accepted suffixes (case-insensitive): tb, gb, mb, kb, b.
// Decimal values are supported (e.g. ".5gb"). "off" and "0" return 0 (disabled).
func parseBackupSize(s string) (int64, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" || s == "0" || s == "off" {
		return 0, nil
	}

	units := []struct {
		suffix string
		bytes  int64
	}{
		{"tb", 1 << 40},
		{"gb", 1 << 30},
		{"mb", 1 << 20},
		{"kb", 1 << 10},
		{"b", 1},
	}

	scale := int64(1)
	numStr := s

	for _, u := range units {
		if strings.HasSuffix(s, u.suffix) {
			scale = u.bytes
			numStr = strings.TrimSuffix(s, u.suffix)

			break
		}
	}

	f, err := strconv.ParseFloat(strings.TrimSpace(numStr), 64)
	if err != nil || f < 0 {
		return 0, fmt.Errorf("invalid backup-size %q: use a number with optional suffix b/kb/mb/gb/tb", s)
	}

	return int64(f * float64(scale)), nil
}

// validateBasePath normalizes and validates a --base-path value shared by
// both commands.Default and commands.Serve. "" and "off" both mean "disabled"
// (mount at the origin root, today's default behavior) and return "". Any
// other value must start with "/" and must not end with "/", so it can be
// spliced directly in front of a route's own leading slash without producing
// a doubled or missing separator (see server.Start's route/appPath helpers).
func validateBasePath(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" || s == offValue {
		return "", nil
	}

	if !strings.HasPrefix(s, "/") {
		return "", fmt.Errorf("invalid base-path %q: must start with \"/\"", s)
	}

	if s == "/" || strings.HasSuffix(s, "/") {
		return "", fmt.Errorf("invalid base-path %q: must not be \"/\" or end with \"/\"", s)
	}

	return s, nil
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
		 --backup-size <size> | off
		 --insecure | -k [true|false]
		 --base-path <path> | off
		 --webauthn [true|false]
			 Turn passkey (Touch ID/Face ID/security key) login on or off for
			 this instance. Requires --webauthn-rp-id and --webauthn-rp-origin
			 to already be set (or given in the same command).
		 --webauthn-rp-id <domain>
			 Bare domain the browser sees, e.g. "issues.example.com".
		 --webauthn-rp-origin <origin>
			 Full browser-facing origin, e.g. "https://issues.example.com".
		 --apns-key-path <path> | off
			 Absolute path to the APNs .p8 auth key. Required together with
			 --apns-key-id, --apns-team-id, and --apns-topic for the server
			 to send push notifications to the iOS/Catalyst app.
		 --apns-key-id <id>
			 APNs auth key ID, from the Apple Developer portal.
		 --apns-team-id <id>
			 Apple Developer Team ID.
		 --apns-topic <bundle-id>
			 APNs topic, i.e. the app's bundle id, e.g. "com.tucats.idtrack".
		 --apns-sandbox [true|false]
			 Use APNs' sandbox environment instead of production.

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
		 --insecure | -k
			 Listen with plain HTTP instead of TLS; no cert/key required. For
			 use behind a TLS-terminating reverse proxy (e.g. nginx).
		 --base-path <path>
			 Mount the whole app (page, assets, API) under this URL prefix
			 instead of the origin root, e.g. "/idtrack".

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
		passkeys <username> list
		passkeys <username> revoke <credential-id>
			Admin escape hatch for a user who has lost their passkey device
			and cannot reach Settings to remove it themselves.

	ingest <file> [file...] [options]
		Bulk-create one issue per input file. All files are validated before
		anything is written — if any file fails to parse, no changes are made.
		 --author <username>              required; reporter of every created issue
		 --default-owner <username>       required; assignee of every created issue
		 --default-project <name>         required; used when a project can't be inferred
		 --default-component <name>       required; used when a component can't be inferred
		 --default-status open|resolved   used when status can't be detected (default: open)
		 --default-priority High|Medium|Low  used when priority can't be detected (default: Medium)
		 --test                           print a report instead of writing to the database

	version
		Print the idtrack version.

`

	fmt.Fprintf(os.Stderr, "\nidtrack %s\n\n", strings.TrimSpace(BuildVersion))
	fmt.Fprintf(os.Stderr, "%s\n\n", strings.TrimSpace(text))
}
