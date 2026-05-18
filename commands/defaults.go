package commands

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

const offValue = "off"

// Default saves one or more settings into ~/.idtrack/defaults.json. When
// called with no arguments it prints the current defaults as a table instead.
// Unspecified keys in the file are left unchanged — existing values are loaded
// and merged on top of them before writing.
func Default(args []string) {
	var (
		port           int
		database       string
		idleTimeout    int
		idleTimeoutSet bool
		appName        string
		appDescription string
		backupInterval string
		backupCount    int
		backupCountSet bool
		backupAge      string
		serverCert     string
		serverCertSet  bool
		serverKey      string
		serverKeySet   bool
	)

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--server-cert", "--cert", "--cert-file":
			if i+1 < len(args) {
				i++
				serverCertSet = true

				if args[i] == offValue {
					serverCert = "" // empty = revert to built-in cert

					continue
				}

				// Ensure this file exists, and make the absolute path to
				// the file.
				if _, err := os.Stat(args[i]); err != nil {
					fmt.Fprintf(os.Stderr, "cannot access server cert file %q: %v\n", args[i], err)
					os.Exit(1)
				}

				abs, err := filepath.Abs(args[i])
				if err != nil {
					fmt.Fprintf(os.Stderr, "cannot resolve path to server cert file %q: %v\n", args[i], err)
					os.Exit(1)
				}

				serverCert = abs
			}

		case "--server-key", "--key", "--key-file":
			if i+1 < len(args) {
				i++
				serverKeySet = true

				if args[i] == offValue {
					serverKey = "" // empty = revert to built-in key

					continue
				}

				// Ensure this file exists, and make the absolute path to
				// the file.
				if _, err := os.Stat(args[i]); err != nil {
					fmt.Fprintf(os.Stderr, "cannot access server key file %q: %v\n", args[i], err)
					os.Exit(1)
				}

				abs, err := filepath.Abs(args[i])
				if err != nil {
					fmt.Fprintf(os.Stderr, "cannot resolve path to server key file %q: %v\n", args[i], err)
					os.Exit(1)
				}

				serverKey = abs
			}

		case "--port", "-p":
			if i+1 < len(args) {
				i++

				n, err := strconv.Atoi(args[i])
				if err != nil {
					fmt.Fprintf(os.Stderr, "invalid port: %s\n", args[i])
					os.Exit(1)
				}

				port = n
			}

		case databaseFlag, "-d":
			if i+1 < len(args) {
				i++
				database = args[i]
			}

		case "--idle-timeout", "-i":
			if i+1 < len(args) {
				i++

				val := args[i]
				if val == "0" || val == "0s" || val == "0m" || val == "0h" || val == offValue {
					idleTimeout = 0
				} else {
					d, err := time.ParseDuration(val)
					if err != nil || d <= 0 {
						fmt.Fprintf(os.Stderr, "invalid idle-timeout %q: use a Go duration like 30m, 1h, 90s\n", val)
						os.Exit(1)
					}

					idleTimeout = int(d.Seconds())
				}

				idleTimeoutSet = true
			}

		case "--app-name":
			if i+1 < len(args) {
				i++
				appName = args[i]
			}

		case "--app-description":
			if i+1 < len(args) {
				i++
				appDescription = args[i]
			}

		case "--backup-interval":
			if i+1 < len(args) {
				i++

				val := args[i]
				if val == "0" || val == "0s" || val == offValue {
					backupInterval = "" // empty = disabled
				} else {
					if _, err := time.ParseDuration(val); err != nil {
						fmt.Fprintf(os.Stderr, "invalid backup-interval %q: use a Go duration like 1h, 30m\n", val)
						os.Exit(1)
					}

					backupInterval = val
				}
			}

		case "--backup-count":
			if i+1 < len(args) {
				i++

				if args[i] == offValue {
					backupCount = 0
					backupCountSet = true

					continue
				}

				n, err := strconv.Atoi(args[i])
				if err != nil || n < 0 {
					fmt.Fprintf(os.Stderr, "invalid backup-count %q: must be a non-negative integer\n", args[i])
					os.Exit(1)
				}

				backupCount = n
				backupCountSet = true
			}

		case "--backup-age":
			if i+1 < len(args) {
				i++

				val := args[i]
				if val == "0" || val == "0s" || val == "0m" || val == "0h" || val == offValue {
					backupAge = "" // empty = disabled
				} else {
					if _, err := time.ParseDuration(val); err != nil {
						fmt.Fprintf(os.Stderr, "invalid backup-age %q: use a Go duration like 168h, 720h\n", val)
						os.Exit(1)
					}

					backupAge = val
				}
			}

		default:
			fmt.Fprintf(os.Stderr, "unknown option: %s\n", args[i])
			Usage()
			os.Exit(1)
		}
	}

	anySet := port != 0 || database != "" || idleTimeoutSet || appName != "" || appDescription != "" ||
		backupInterval != "" || backupCountSet || backupAge != "" || serverCertSet || serverKeySet
	if !anySet {
		showDefaults()

		return
	}

	// Load current saved defaults so we preserve any keys we are not updating.
	defs := loadDefaults()

	if serverCertSet {
		defs.ServerCert = serverCert // "" clears the setting (reverts to built-in)
	}

	if serverKeySet {
		defs.ServerKey = serverKey // "" clears the setting (reverts to built-in)
	}

	if port != 0 {
		defs.Port = port
	}

	if database != "" {
		// Resolve to an absolute path before saving so that the stored value
		// is not sensitive to the working directory of future invocations.
		if abs, err := filepath.Abs(database); err == nil {
			database = abs
		}
		
		defs.Database = database
	}

	if idleTimeoutSet {
		defs.IdleTimeout = idleTimeout
	}

	if appName != "" {
		defs.AppName = appName
	}

	if appDescription != "" {
		defs.AppDescription = appDescription
	}

	if backupInterval != "" {
		defs.BackupInterval = backupInterval
	}

	if backupCountSet {
		defs.BackupCount = backupCount
	}

	if backupAge != "" {
		defs.BackupAge = backupAge
	}

	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot determine home directory: %v\n", err)
		os.Exit(1)
	}

	dir := filepath.Join(home, ".idtrack")
	if err := os.MkdirAll(dir, 0700); err != nil {
		fmt.Fprintf(os.Stderr, "cannot create %s: %v\n", dir, err)
		os.Exit(1)
	}

	path := filepath.Join(dir, "defaults.json")

	data, err := json.MarshalIndent(defs, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot encode defaults: %v\n", err)
		os.Exit(1)
	}

	if err := os.WriteFile(path, append(data, '\n'), 0600); err != nil {
		fmt.Fprintf(os.Stderr, "cannot write %s: %v\n", path, err)
		os.Exit(1)
	}

	fmt.Printf("defaults written to %s\n", path)

	if defs.Port != 0 {
		fmt.Printf("  port:            %d\n", defs.Port)
	}

	if defs.Database != "" {
		fmt.Printf("  database:        %s\n", defs.Database)
	}

	if defs.IdleTimeout > 0 {
		fmt.Printf("  idle-timeout:    %s\n", time.Duration(defs.IdleTimeout)*time.Second)
	} else if idleTimeoutSet {
		fmt.Printf("  idle-timeout:    disabled\n")
	}

	if defs.AppName != "" {
		fmt.Printf("  app-name:        %s\n", defs.AppName)
	}

	if defs.AppDescription != "" {
		fmt.Printf("  app-description: %s\n", defs.AppDescription)
	}

	if defs.BackupInterval != "" {
		fmt.Printf("  backup-interval: %s\n", defs.BackupInterval)
	}

	if defs.BackupCount > 0 {
		fmt.Printf("  backup-count:    %d\n", defs.BackupCount)
	}

	if defs.BackupAge != "" {
		fmt.Printf("  backup-age:      %s\n", defs.BackupAge)
	}
}

// showDefaults prints the current contents of ~/.idtrack/defaults.json as a
// two-column table. Called when Default is invoked with no flags.
func showDefaults() {
	defs := loadDefaults()

	home, _ := os.UserHomeDir()
	path := filepath.Join(home, ".idtrack", "defaults.json")
	fmt.Printf("Current defaults (%s):\n\n", path)

	row := func(setting, value string) {
		fmt.Printf("  %-20s %s\n", setting, value)
	}

	if defs.Port != 0 {
		row("port", fmt.Sprintf("%d", defs.Port))
	} else {
		row("port", "8443 (default)")
	}

	if defs.Database != "" {
		row("database", defs.Database)
	} else {
		row("database", "(not set)")
	}

	if defs.ServerCert != "" {
		row("server-cert", defs.ServerCert)
	} else {
		row("server-cert", "(embedded self-signed)")
	}

	if defs.ServerKey != "" {
		row("server-key", defs.ServerKey)
	} else {
		row("server-key", "(embedded self-signed)")
	}

	if defs.IdleTimeout > 0 {
		row("idle-timeout", (time.Duration(defs.IdleTimeout) * time.Second).String())
	} else {
		row("idle-timeout", "disabled")
	}

	if defs.AppName != "" {
		row("app-name", defs.AppName)
	} else {
		row("app-name", "(not set)")
	}

	if defs.AppDescription != "" {
		row("app-description", defs.AppDescription)
	} else {
		row("app-description", "(not set)")
	}

	if defs.BackupInterval != "" {
		row("backup-interval", defs.BackupInterval)
	} else {
		row("backup-interval", "disabled")
	}

	if defs.BackupCount > 0 {
		row("backup-count", fmt.Sprintf("%d", defs.BackupCount))
	} else {
		row("backup-count", "no limit")
	}

	if defs.BackupAge != "" {
		row("backup-age", defs.BackupAge)
	} else {
		row("backup-age", "no limit")
	}
}
