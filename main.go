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

	"github.com/tucats/idtrack/db"
	"github.com/tucats/idtrack/server"
)

//go:embed resources
var embedded embed.FS

type defaults struct {
	Port     int    `json:"port"`
	Database string `json:"database"`
}

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		usage()
		os.Exit(1)
	}

	switch args[0] {
	case "serve":
		runServe(args[1:])
	case "stop":
		runStop()
	case "default":
		runDefault(args[1:])
	case "user":
		runUser(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown verb: %s\n", args[0])
		usage()
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage:")
	fmt.Fprintln(os.Stderr, "  idtrack default [--port n] [--database path]")
	fmt.Fprintln(os.Stderr, "  idtrack serve [--port n] [--database path]")
	fmt.Fprintln(os.Stderr, "  idtrack stop")
	fmt.Fprintln(os.Stderr, "  idtrack user --list [--database path]")
	fmt.Fprintln(os.Stderr, "  idtrack user --add username:password [--name text] [--admin true|false] [--database path]")
	fmt.Fprintln(os.Stderr, "  idtrack user --update username [--name text] [--password text] [--admin true|false] [--database path]")
	fmt.Fprintln(os.Stderr, "  idtrack user --delete username [--database path]")
}

func runServe(args []string) {
	defs := loadDefaults()
	port := defs.Port
	database := defs.Database
	foreground := false

	var passArgs []string // args forwarded to background child (without --foreground)
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--foreground":
			foreground = true
		case "--port":
			if i+1 < len(args) {
				i++
				n, err := strconv.Atoi(args[i])
				if err != nil {
					fmt.Fprintf(os.Stderr, "invalid port: %s\n", args[i])
					os.Exit(1)
				}
				port = n
				passArgs = append(passArgs, "--port", args[i])
			}
		case "--database":
			if i+1 < len(args) {
				i++
				database = args[i]
				passArgs = append(passArgs, "--database", args[i])
			}
		default:
			fmt.Fprintf(os.Stderr, "unknown option: %s\n", args[i])
			usage()
			os.Exit(1)
		}
	}

	if database == "" {
		database = "idtrack.db"
	}
	if port == 0 {
		port = 8443
	}

	if !foreground {
		launchBackground(passArgs)
		return
	}

	d, err := db.Open(database)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error opening database %q: %v\n", database, err)
		os.Exit(1)
	}

	static := fs.FS(embedded)
	if err := server.Start(d, port, static); err != nil {
		fmt.Fprintf(os.Stderr, "server error: %v\n", err)
		os.Exit(1)
	}
}

func launchBackground(serveArgs []string) {
	pidFile := serverPidPath()

	// Detect a running server before starting a new one.
	if data, err := os.ReadFile(pidFile); err == nil {
		if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil {
			if proc, err := os.FindProcess(pid); err == nil {
				if proc.Signal(syscall.Signal(0)) == nil {
					fmt.Fprintf(os.Stderr, "server already running (pid %d)\n", pid)
					os.Exit(1)
				}
			}
		}
		os.Remove(pidFile) // stale PID file
	}

	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot locate executable: %v\n", err)
		os.Exit(1)
	}

	logPath := serverLogPath()
	if err := os.MkdirAll(filepath.Dir(logPath), 0700); err != nil {
		fmt.Fprintf(os.Stderr, "cannot create log directory: %v\n", err)
		os.Exit(1)
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot open log file: %v\n", err)
		os.Exit(1)
	}

	childArgs := append([]string{"serve", "--foreground"}, serveArgs...)
	cmd := exec.Command(exe, childArgs...)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	if err := cmd.Start(); err != nil {
		logFile.Close()
		fmt.Fprintf(os.Stderr, "error starting server: %v\n", err)
		os.Exit(1)
	}
	logFile.Close()

	if err := os.WriteFile(pidFile, []byte(strconv.Itoa(cmd.Process.Pid)), 0600); err != nil {
		fmt.Fprintf(os.Stderr, "cannot write pid file: %v\n", err)
	}

	fmt.Printf("idtrack server started (pid %d)\n", cmd.Process.Pid)
	fmt.Printf("log: %s\n", logPath)
}

func runStop() {
	pidFile := serverPidPath()
	data, err := os.ReadFile(pidFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "no server running (pid file not found)")
		os.Exit(1)
	}

	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		fmt.Fprintln(os.Stderr, "invalid pid file")
		os.Remove(pidFile)
		os.Exit(1)
	}

	proc, err := os.FindProcess(pid)
	if err != nil {
		fmt.Fprintf(os.Stderr, "process %d not found\n", pid)
		os.Remove(pidFile)
		os.Exit(1)
	}

	if err := proc.Signal(syscall.SIGTERM); err != nil {
		fmt.Fprintf(os.Stderr, "error stopping server (pid %d): %v\n", pid, err)
		os.Exit(1)
	}

	os.Remove(pidFile)
	fmt.Printf("idtrack server stopped (pid %d)\n", pid)
}

func serverPidPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".idtrack", "idtrack.pid")
}

func serverLogPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".idtrack", "idtrack.log")
}

func runUser(args []string) {
	var add, del, update, name, password, database, adminStr string
	var list bool

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--list":
			list = true
		case "--add":
			if i+1 < len(args) {
				i++
				add = args[i]
			}
		case "--delete":
			if i+1 < len(args) {
				i++
				del = args[i]
			}
		case "--update":
			if i+1 < len(args) {
				i++
				update = args[i]
			}
		case "--name":
			if i+1 < len(args) {
				i++
				name = args[i]
			}
		case "--password":
			if i+1 < len(args) {
				i++
				password = args[i]
			}
		case "--admin":
			if i+1 < len(args) {
				i++
				adminStr = args[i]
				if adminStr != "true" && adminStr != "false" {
					fmt.Fprintln(os.Stderr, "--admin requires true or false")
					os.Exit(1)
				}
			}
		case "--database":
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
		fmt.Fprintln(os.Stderr, "must specify --list, --add, --update, or --delete")
		usage()
		os.Exit(1)
	}

	if database == "" {
		defs := loadDefaults()
		database = defs.Database
	}
	if database == "" {
		database = "idtrack.db"
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
		parts := strings.SplitN(add, ":", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			fmt.Fprintln(os.Stderr, "--add requires username:password")
			os.Exit(1)
		}
		username, pwd := parts[0], parts[1]
		displayName := username
		if name != "" {
			displayName = name
		}
		hash := fmt.Sprintf("%x", sha256.Sum256([]byte(pwd)))
		if err := db.AddUser(d, username, displayName, hash, adminStr == "true"); err != nil {
			fmt.Fprintf(os.Stderr, "error adding user %q: %v\n", username, err)
			os.Exit(1)
		}
		fmt.Printf("user %q added\n", username)
	}

	if update != "" {
		if name == "" && password == "" && adminStr == "" {
			fmt.Fprintln(os.Stderr, "--update requires at least --name, --password, or --admin")
			usage()
			os.Exit(1)
		}
		var hash string
		if password != "" {
			hash = fmt.Sprintf("%x", sha256.Sum256([]byte(password)))
		}
		var adminPtr *bool
		if adminStr != "" {
			val := adminStr == "true"
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

func runDefault(args []string) {
	var port int
	var database string

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--port":
			if i+1 < len(args) {
				i++
				n, err := strconv.Atoi(args[i])
				if err != nil {
					fmt.Fprintf(os.Stderr, "invalid port: %s\n", args[i])
					os.Exit(1)
				}
				port = n
			}
		case "--database":
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

	if port == 0 && database == "" {
		fmt.Fprintln(os.Stderr, "must specify at least --port or --database")
		usage()
		os.Exit(1)
	}

	// Merge with existing defaults so unspecified values are preserved.
	defs := loadDefaults()
	if port != 0 {
		defs.Port = port
	}
	if database != "" {
		defs.Database = database
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
		fmt.Printf("  port:     %d\n", defs.Port)
	}
	if defs.Database != "" {
		fmt.Printf("  database: %s\n", defs.Database)
	}
}

func loadDefaults() defaults {
	home, err := os.UserHomeDir()
	if err != nil {
		return defaults{}
	}
	data, err := os.ReadFile(filepath.Join(home, ".idtrack", "defaults.json"))
	if err != nil {
		return defaults{}
	}
	var d defaults
	json.Unmarshal(data, &d)
	return d
}
