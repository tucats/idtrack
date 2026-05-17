// Package main is the CLI entry point for idtrack. It dispatches sub-commands
// (serve, stop, user, define, delete, default, version) and contains the logic
// for background-process management and user/project administration.
package main

import (
	"crypto/sha256"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/tucats/idtrack/db"
	"github.com/tucats/idtrack/server"
)

const (
	databaseFlag = "--database"
	defaultDB    = "idtrack.db"
	trueValue    = "true"
)

// BuildVersion and BuildTime are set at link time by the build script with
// -ldflags "-X main.BuildVersion=... -X main.BuildTime=...".
// When you run a plain "go build" without those flags, they keep their default
// values so the binary still works — it just shows "dev" for the version.
var BuildVersion = "dev"
var BuildTime = ""

// embedded holds the contents of the resources/ directory, compiled directly
// into the binary. The //go:embed directive tells the Go toolchain to include
// every file under resources/ in this variable at build time. At runtime we
// read from it with fs.ReadFile — no files need to be present on disk.
//
//go:embed resources
var embedded embed.FS

// defaults holds the persisted user preferences stored in ~/.idtrack/defaults.json.
// The `json:"..."` struct tags control how each field is serialized: "port"
// becomes the JSON key for Port, and "database" for Database.
type defaults struct {
	Port           int    `json:"port"`
	Database       string `json:"database"`
	IdleTimeout    int    `json:"idle_timeout,omitempty"`    // seconds; 0 means disabled
	AppName        string `json:"app_name,omitempty"`        // custom branding name
	AppDescription string `json:"app_description,omitempty"` // custom branding tagline
}

// pidRecord is the structure written to ~/.idtrack/idtrack.pid. Storing the
// serve args alongside the PID allows "idtrack restart" to relaunch the server
// with exactly the same flags that were passed to the original "idtrack serve".
type pidRecord struct {
	PID  int      `json:"pid"`
	Args []string `json:"args"` // passArgs forwarded to the background child
}

// readPidFile reads and parses the PID file. It understands both the current
// JSON format and the legacy plain-integer format, so existing PID files from
// older builds remain usable (they just have no saved args for restart).
func readPidFile() (pidRecord, error) {
	data, err := os.ReadFile(serverPidPath())
	if err != nil {
		return pidRecord{}, err
	}

	var record pidRecord
	if err := json.Unmarshal(data, &record); err != nil {
		// Legacy format: just a bare PID integer with no JSON wrapper.
		pid, err2 := strconv.Atoi(strings.TrimSpace(string(data)))
		if err2 != nil {
			return pidRecord{}, fmt.Errorf("unreadable pid file")
		}

		return pidRecord{PID: pid}, nil
	}

	return record, nil
}

// main is the program entry point. os.Args[0] is the program name, so we skip
// it and inspect the first real argument (args[0]) to decide which sub-command
// to run. Each case delegates to a dedicated function, keeping main small and
// easy to scan.
func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		usage()
		os.Exit(1)
	}

	switch args[0] {
	case "help", "--help", "-h":
		usage()
		os.Exit(0)
	case "serve", "start", "run":
		runServe(args[1:])
	case "stop":
		runStop()
	case "restart":
		runRestart()
	case "default", "config":
		runDefault(args[1:])
	case "user", "users":
		runUser(args[1:])
	case "define":
		runDefine(args[1:])
	case "delete":
		runDeleteProjectOrComponent(args[1:])
	case "version", "-v", "--version":
		runVersion()
	default:
		fmt.Fprintf(os.Stderr, "unknown verb: %s\n", args[0])
		usage()
		os.Exit(1)
	}
}

// usage prints a summary of available sub-commands to stderr. It is called
// when the user provides no arguments or an unrecognized verb.
func usage() {
	text := `
idtrack is a self-hosted issue tracker for small teams. It provides a web UI 
for managing projects, components, and issues.

Usage:

	idtrack [command] [options]

Commands: 

	default [options]
		Set default values for options which are used if not overridden.
		 --port <n>
		 --database <path>
		 --idle-timeout <duration>
		 --app-name <name>
		 --app-description <text>

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

// runVersion prints the version string. When the build script injects
// BuildTime the output includes the build timestamp; otherwise it is omitted.
func runVersion() {
	if BuildTime != "" {
		fmt.Printf("idtrack version %s (built %s)\n", BuildVersion, BuildTime)
	} else {
		fmt.Printf("idtrack version %s\n", BuildVersion)
	}
}

// runServe handles the "serve" sub-command. It parses flags, applies defaults,
// and then either launches the server in the background (the normal case) or
// runs it directly in the foreground when --foreground is present.
//
// The two-mode design exists because Go has no clean fork() equivalent.
// Instead the parent process re-executes itself with "--foreground" as a
// background child, so the server outlives the terminal that launched it.
func runServe(args []string) {
	var passArgs []string

	defs := loadDefaults()
	port := defs.Port
	database := defs.Database
	foreground := false

	// passArgs collects flags that must be forwarded to the background child.
	// We exclude --foreground because the child adds it itself.

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--foreground":
			foreground = true
		case "--port", "-p":
			if i+1 < len(args) {
				i++ // consume the next element as the flag value

				n, err := strconv.Atoi(args[i])
				if err != nil {
					fmt.Fprintf(os.Stderr, "invalid port: %s\n", args[i])
					os.Exit(1)
				}

				port = n

				passArgs = append(passArgs, "--port", args[i])
			}

		case databaseFlag:
			if i+1 < len(args) {
				i++
				database = args[i]
				passArgs = append(passArgs, databaseFlag, args[i])
			}

		default:
			fmt.Fprintf(os.Stderr, "unknown option: %s\n", args[i])
			usage()
			os.Exit(1)
		}
	}

	if database == "" {
		database = defaultDB
	}

	if port == 0 {
		port = 8443
	}

	// If we are not running in foreground mode, spawn a detached child process
	// and exit. The child will call runServe again with --foreground set.
	if !foreground {
		launchBackground(passArgs)

		return
	}

	// Foreground path: open the database and block in the HTTP server loop.
	d, err := db.Open(database)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error opening database %q: %v\n", database, err)
		os.Exit(1)
	}

	// fs.FS(embedded) converts our embed.FS to the standard fs.FS interface
	// that server.Start expects, allowing it to read static files.
	static := fs.FS(embedded)
	if err := server.Start(d, port, static, BuildVersion, BuildTime, defs.IdleTimeout, defs.AppName, defs.AppDescription); err != nil {
		fmt.Fprintf(os.Stderr, "server error: %v\n", err)
		os.Exit(1)
	}
}

// launchBackground re-executes this binary as a detached background process.
// It prevents duplicate servers by checking the PID file for a running process,
// then redirects child stdout/stderr to the log file and writes the child's PID.
func launchBackground(serveArgs []string) {
	pidFile := serverPidPath()

	// Check if a server is already running. Signal 0 tests process existence
	// without actually sending a signal — if it succeeds, the process is alive.
	if record, err := readPidFile(); err == nil {
		if proc, err := os.FindProcess(record.PID); err == nil {
			if proc.Signal(syscall.Signal(0)) == nil {
				fmt.Fprintf(os.Stderr, "server already running (pid %d)\n", record.PID)
				os.Exit(1)
			}
		}

		os.Remove(pidFile) // PID file exists but process is gone — clean it up
	}

	// os.Executable() returns the path of the currently running binary so we
	// can re-exec it without depending on PATH.
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot locate executable: %v\n", err)
		os.Exit(1)
	}

	logPath := serverLogPath()
	// MkdirAll creates the directory and any missing parents (like mkdir -p).
	// 0700 means only the owner can read/write/enter — appropriate for a
	// private config directory.
	if err := os.MkdirAll(filepath.Dir(logPath), 0700); err != nil {
		fmt.Fprintf(os.Stderr, "cannot create log directory: %v\n", err)
		os.Exit(1)
	}
	// Open in append mode so repeated server restarts accumulate logs rather
	// than overwriting them. 0600 = owner read/write only.
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot open log file: %v\n", err)
		os.Exit(1)
	}

	childArgs := append([]string{"serve", "--foreground"}, serveArgs...)
	cmd := exec.Command(exe, childArgs...)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	// Setsid creates a new session for the child, detaching it from the
	// parent's process group. This means the child survives when the terminal
	// (and therefore the parent) closes.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	if err := cmd.Start(); err != nil {
		logFile.Close()
		fmt.Fprintf(os.Stderr, "error starting server: %v\n", err)
		os.Exit(1)
	}

	logFile.Close() // parent no longer needs the file; child inherited its own fd

	// Record the child's PID and the serve args so "idtrack stop" can find and
	// terminate the process, and "idtrack restart" can relaunch with the same flags.
	record := pidRecord{PID: cmd.Process.Pid, Args: serveArgs}
	if pidData, err := json.Marshal(record); err == nil {
		if err := os.WriteFile(pidFile, pidData, 0600); err != nil {
			fmt.Fprintf(os.Stderr, "cannot write pid file: %v\n", err)
		}
	}

	fmt.Printf("idtrack server started (pid %d)\n", cmd.Process.Pid)
	fmt.Printf("log: %s\n", logPath)
}

// runStop reads the PID file written by launchBackground, sends SIGTERM to
// the server process, and removes the PID file. SIGTERM is the conventional
// "please shut down gracefully" signal on Unix systems.
func runStop() {
	pidFile := serverPidPath()

	record, err := readPidFile()
	if err != nil {
		fmt.Fprintln(os.Stderr, "no server running (pid file not found)")
		os.Exit(1)
	}

	proc, err := os.FindProcess(record.PID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "process %d not found\n", record.PID)
		os.Remove(pidFile)
		os.Exit(1)
	}

	if err := proc.Signal(syscall.SIGTERM); err != nil {
		fmt.Fprintf(os.Stderr, "error stopping server (pid %d): %v\n", record.PID, err)
		os.Exit(1)
	}

	os.Remove(pidFile)
	fmt.Printf("idtrack server stopped (pid %d)\n", record.PID)
}

// runRestart stops the currently running server and immediately relaunches it
// with the same command-line arguments recorded in the PID file. It waits for
// the old process to exit before starting the new one so the port is free.
func runRestart() {
	pidFile := serverPidPath()

	record, err := readPidFile()
	if err != nil {
		fmt.Fprintln(os.Stderr, "no server running (pid file not found)")
		os.Exit(1)
	}

	proc, err := os.FindProcess(record.PID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "process %d not found\n", record.PID)
		os.Remove(pidFile)
		os.Exit(1)
	}

	if err := proc.Signal(syscall.SIGTERM); err != nil {
		fmt.Fprintf(os.Stderr, "error stopping server (pid %d): %v\n", record.PID, err)
		os.Exit(1)
	}

	fmt.Printf("idtrack server stopped (pid %d)\n", record.PID)

	// Wait for the old process to fully exit before relaunching. Without this,
	// the new child may fail to bind the same port while the old one still holds it.
	// We poll with signal 0 (existence check) up to a 10-second deadline.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(250 * time.Millisecond)

		if proc.Signal(syscall.Signal(0)) != nil {
			break // non-nil error means the process no longer exists
		}
	}

	os.Remove(pidFile)

	if len(record.Args) > 0 {
		fmt.Printf("restarting with args: %s\n", strings.Join(record.Args, " "))
	} else {
		fmt.Println("restarting...")
	}

	launchBackground(record.Args)
}

// serverPidPath returns the full path of the PID file used to track the
// running server process (~/.idtrack/idtrack.pid).
func serverPidPath() string {
	home, _ := os.UserHomeDir()

	return filepath.Join(home, ".idtrack", "idtrack.pid")
}

// serverLogPath returns the full path of the server log file
// (~/.idtrack/idtrack.log).
func serverLogPath() string {
	home, _ := os.UserHomeDir()

	return filepath.Join(home, ".idtrack", "idtrack.log")
}

// runUser handles the "user" sub-command. A single invocation may perform
// only one of list, add, delete, ur update. The flags are parsed
// first, validated, and only then is the database opened — this avoids
// creating the DB file for a bad invocation.
func runUser(args []string) {
	var (
		add, del, update, name, password, database, adminStr string
		list                                                 bool
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

				// If it's not a legit version of true/false, complain. Otherwise, use
				// normalized "true"/"false" strings to avoid ambiguity in UpdateUser's
				// *bool parameter.
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
			usage()
			os.Exit(1)
		}
	}

	if !list && add == "" && del == "" && update == "" {
		fmt.Fprintln(os.Stderr, "must specify list, add, update, or delete")
		usage()
		os.Exit(1)
	}

	// Fall back to saved defaults if --database was not provided.
	if database == "" {
		defs := loadDefaults()
		database = defs.Database
	}

	if database == "" {
		database = defaultDB
	}

	d, err := db.Open(database)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error opening database %q: %v\n", database, err)
		os.Exit(1)
	}

	// defer ensures d.Close() is called when runUser returns, even if we exit
	// via an error path. This releases the SQLite file lock cleanly.
	defer d.Close()

	if list {
		users, err := db.ListUsers(d)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error listing users: %v\n", err)
			os.Exit(1)
		}

		// %-20s left-aligns the string in a field 20 characters wide.
		fmt.Printf("%-20s  %-30s  %-7s  %s\n", "USERNAME", "DISPLAY NAME", "ADMIN", "LAST LOGIN")
		fmt.Printf("%-20s  %-30s  %-7s  %s\n", strings.Repeat("-", 20), strings.Repeat("-", 30), strings.Repeat("-", 7), strings.Repeat("-", 25))

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
		// The --add value must be "username:password". SplitN with n=2 ensures
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
		// SHA-256 hash the password before storing it. The browser also hashes
		// the password with SHA-256 before sending it over the wire, so the
		// hash stored here is the credential that Basic Auth will compare.
		// %x formats the [32]byte array as lowercase hex.
		hash := fmt.Sprintf("%x", sha256.Sum256([]byte(pwd)))
		if err := db.AddUser(d, username, displayName, hash, adminStr == trueValue); err != nil {
			fmt.Fprintf(os.Stderr, "error adding user %q: %v\n", username, err)
			os.Exit(1)
		}

		fmt.Printf("user %q added\n", username)
	}

	if update != "" {
		if name == "" && password == "" && adminStr == "" {
			fmt.Fprintln(os.Stderr, "update requires at least --name, --password, or --admin")
			usage()
			os.Exit(1)
		}

		var hash string

		if password != "" {
			hash = fmt.Sprintf("%x", sha256.Sum256([]byte(password)))
		}

		// db.UpdateUser uses *bool (a pointer to bool) for the admin flag so
		// that nil means "not specified" — a plain bool has no way to represent
		// "the caller did not pass this flag".
		var adminPtr *bool

		if adminStr != "" {
			val := adminStr == trueValue
			adminPtr = &val
		}

		if err := db.UpdateUser(d, update, name, hash, adminPtr); err != nil {
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

// runDefine handles the "define" sub-command. The first positional argument is
// a subcommand: "project" (creates a new project) or "component" (adds a
// component to an existing project). Both operations are idempotent.
func runDefine(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "define requires a subcommand: project or component")
		usage()
		os.Exit(1)
	}

	subcommand := args[0]
	rest := args[1:]

	var project, component, database string

	switch subcommand {
	case "project":
		if len(rest) == 0 || strings.HasPrefix(rest[0], "--") {
			fmt.Fprintln(os.Stderr, "define project requires a project name")
			usage()
			os.Exit(1)
		}

		project = rest[0]
		rest = rest[1:]

	case "component":
		if len(rest) < 2 || strings.HasPrefix(rest[0], "--") || strings.HasPrefix(rest[1], "--") {
			fmt.Fprintln(os.Stderr, "define component requires a project name and a component name")
			usage()
			os.Exit(1)
		}

		project = rest[0]
		component = rest[1]
		rest = rest[2:]

	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n", subcommand)
		usage()
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
			usage()
			os.Exit(1)
		}
	}

	if database == "" {
		defs := loadDefaults()
		database = defs.Database
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

// runDeleteProjectOrComponent handles the "delete" sub-command. The first
// positional argument is a subcommand: "project" (removes the whole project
// and all its components) or "component" (removes one component). Both refuse
// to delete if any issues reference the target, returning the blocking IDs.
func runDeleteProjectOrComponent(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "delete requires a subcommand: project or component")
		usage()
		os.Exit(1)
	}

	subcommand := args[0]
	rest := args[1:]

	var project, component, database string

	switch subcommand {
	case "project":
		if len(rest) == 0 || strings.HasPrefix(rest[0], "--") {
			fmt.Fprintln(os.Stderr, "delete project requires a project name")
			usage()
			os.Exit(1)
		}

		project = rest[0]
		rest = rest[1:]

	case "component":
		if len(rest) < 2 || strings.HasPrefix(rest[0], "--") || strings.HasPrefix(rest[1], "--") {
			fmt.Fprintln(os.Stderr, "delete component requires a project name and a component name")
			usage()
			os.Exit(1)
		}

		project = rest[0]
		component = rest[1]
		rest = rest[2:]

	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n", subcommand)
		usage()
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
			usage()
			os.Exit(1)
		}
	}

	if database == "" {
		defs := loadDefaults()
		database = defs.Database
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

// runDefault saves port and/or database path into ~/.idtrack/defaults.json so
// that subsequent commands do not need those flags. Unspecified values in the
// file are left unchanged — we load existing values and merge on top of them.
func runDefault(args []string) {
	var (
		port           int
		database       string
		idleTimeout    int
		idleTimeoutSet bool
		appName        string
		appDescription string
	)

	for i := 0; i < len(args); i++ {
		switch args[i] {
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
				if val == "0" || val == "0s" || val == "0m" || val == "0h" {
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
		default:
			fmt.Fprintf(os.Stderr, "unknown option: %s\n", args[i])
			usage()
			os.Exit(1)
		}
	}

	if port == 0 && database == "" && !idleTimeoutSet && appName == "" && appDescription == "" {
		fmt.Fprintln(os.Stderr, "must specify at least --port, --database, --idle-timeout, --app-name, or --app-description")
		usage()
		os.Exit(1)
	}

	// Load current saved defaults so we preserve any keys we are not updating.
	defs := loadDefaults()
	if port != 0 {
		defs.Port = port
	}

	if database != "" {
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
		fmt.Printf("  port:         %d\n", defs.Port)
	}

	if defs.Database != "" {
		fmt.Printf("  database:     %s\n", defs.Database)
	}

	if defs.IdleTimeout > 0 {
		fmt.Printf("  idle-timeout: %s\n", time.Duration(defs.IdleTimeout)*time.Second)
	} else if idleTimeoutSet {
		fmt.Printf("  idle-timeout: disabled\n")
	}

	if defs.AppName != "" {
		fmt.Printf("  app-name:     %s\n", defs.AppName)
	}

	if defs.AppDescription != "" {
		fmt.Printf("  app-desc:     %s\n", defs.AppDescription)
	}
}

// loadDefaults reads ~/.idtrack/defaults.json and returns its contents as a
// defaults struct. If the file does not exist or cannot be read, an empty
// struct is returned so callers can apply their own fallback values.
func loadDefaults() defaults {
	var d defaults

	home, err := os.UserHomeDir()
	if err != nil {
		return defaults{}
	}

	data, err := os.ReadFile(filepath.Join(home, ".idtrack", "defaults.json"))
	if err != nil {
		return defaults{} // file not yet created — silently use zero values
	}

	json.Unmarshal(data, &d) // ignore parse error; zero struct is a safe fallback

	return d
}
