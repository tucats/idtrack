package commands

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
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

// pidRecord is written to ~/.idtrack/idtrack.pid. Storing the serve args
// alongside the PID allows Restart to relaunch the server with exactly the
// same flags that were passed to the original Serve call.
type pidRecord struct {
	PID     int      `json:"pid"`
	Version string   `json:"version,omitempty"`
	Path    string   `json:"path"`
	Args    []string `json:"args"` // passArgs forwarded to the background child
}

// readPidFile reads and parses the PID file. It understands both the current
// JSON format and the legacy plain-integer format, so existing PID files from
// older builds remain usable (they just have no saved args for Restart).
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

// serverPidPath returns the full path of the PID file (~/.idtrack/idtrack.pid).
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

// Serve handles the "serve" sub-command. It parses flags, applies defaults,
// and then either launches the server in the background (the normal case) or
// runs it directly in the foreground when --foreground is present.
//
// The two-mode design exists because Go has no clean fork() equivalent.
// Instead the parent process re-executes itself with "--foreground" as a
// background child, so the server outlives the terminal that launched it.
//
// static is the embedded fs.FS passed from main — it cannot live in this
// package because //go:embed must be declared alongside the resources directory.
func Serve(args []string, static fs.FS) {
	var (
		passArgs []string
	)

	defs := loadDefaults()
	port := defs.Port
	keyFile := defs.ServerKey
	certFile := defs.ServerCert
	database := defs.Database
	insecure := defs.Insecure
	basePath := defs.BasePath
	foreground := false

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--server-cert", "--cert", "--cert-file":
			if i+1 < len(args) {
				i++

				certFile = args[i]
				if abs, err := filepath.Abs(args[i]); err == nil {
					certFile = abs
				} else {
					fmt.Fprintf(os.Stderr, "cannot resolve path to server cert file %q: %v\n", args[i], err)
					os.Exit(1)
				}

				passArgs = append(passArgs, "--cert-file", args[i])
			}

		case "--server-key", "--key", "--key-file":
			if i+1 < len(args) {
				i++

				keyFile = args[i]
				if abs, err := filepath.Abs(args[i]); err == nil {
					keyFile = abs
				} else {
					fmt.Fprintf(os.Stderr, "cannot resolve path to server key file %q: %v\n", args[i], err)
					os.Exit(1)
				}

				passArgs = append(passArgs, "--key-file", args[i])
			}

		case "--foreground":
			foreground = true

		case "--insecure", "-k":
			insecure = true
			passArgs = append(passArgs, "--insecure")

			// Optional explicit value lets the setting be turned back off
			// (overriding a stored default of insecure=true) without a
			// separate flag; a bare "--insecure" is the common case.
			if i+1 < len(args) {
				switch strings.ToLower(args[i+1]) {
				case "false", "off":
					insecure = false
					passArgs = append(passArgs, "false")
					i++
				case "true", "on":
					passArgs = append(passArgs, "true")
					i++
				}
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

				passArgs = append(passArgs, "--port", args[i])
			}

		case databaseFlag:
			if i+1 < len(args) {
				i++
				database = args[i]
				passArgs = append(passArgs, databaseFlag, args[i])
			}

		case "--base-path":
			if i+1 < len(args) {
				i++

				v, err := validateBasePath(args[i])
				if err != nil {
					fmt.Fprintf(os.Stderr, "%v\n", err)
					os.Exit(1)
				}

				basePath = v
				passArgs = append(passArgs, "--base-path", v)
			}

		default:
			fmt.Fprintf(os.Stderr, "unknown option: %s\n", args[i])
			Usage()
			os.Exit(1)
		}
	}

	if database == "" {
		database = defaultDB
	}

	if port == 0 {
		port = 8443
	}

	scheme := "https"
	if insecure {
		scheme = "http"
	}

	// appPath mirrors server.srv.appPath(): the app is mounted at basePath
	// when one is configured, or the historical "/idtrack" otherwise.
	appPath := "/idtrack"
	if basePath != "" {
		appPath = basePath
	}

	host, _ := os.Hostname()
	url := fmt.Sprintf("%s://%s%s", scheme, net.JoinHostPort(host, strconv.Itoa(port)), appPath)

	// If we are not running in foreground mode, spawn a detached child process
	// and exit. The child will call Serve again with --foreground set.
	if !foreground {
		launchBackground(passArgs, url)

		return
	}

	// Foreground path: open the database and block in the HTTP server loop.
	absDB, err := filepath.Abs(database)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot resolve database path %q: %v\n", database, err)
		os.Exit(1)
	}

	d, err := db.Open(absDB)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error opening database %q: %v\n", absDB, err)
		os.Exit(1)
	}

	// Parse backup duration strings from stored defaults. Invalid values are
	// ignored silently — they were validated when written by Default.
	var backupInterval, backupAge time.Duration
	if defs.BackupInterval != "" {
		backupInterval, _ = time.ParseDuration(defs.BackupInterval)
	}

	if defs.BackupAge != "" {
		backupAge, _ = time.ParseDuration(defs.BackupAge)
	}

	var backupSize int64
	if defs.BackupSize != "" {
		backupSize, _ = parseBackupSize(defs.BackupSize)
	}

	cfg := server.Config{
		Database:         d,
		Port:             port,
		Static:           static,
		Version:          BuildVersion,
		BuildTime:        BuildTime,
		IdleTimeout:      defs.IdleTimeout,
		AppName:          defs.AppName,
		AppDescription:   defs.AppDescription,
		DBPath:           absDB,
		BackupInterval:   backupInterval,
		BackupCount:      defs.BackupCount,
		BackupAge:        backupAge,
		BackupSize:       backupSize,
		CertFile:         certFile,
		KeyFile:          keyFile,
		Insecure:         insecure,
		BasePath:         basePath,
		WebAuthnEnabled:  defs.WebAuthn,
		WebAuthnRPID:     defs.WebAuthnRPID,
		WebAuthnRPOrigin: defs.WebAuthnRPOrigin,
		ApnsKeyPath:      defs.ApnsKeyPath,
		ApnsKeyID:        defs.ApnsKeyID,
		ApnsTeamID:       defs.ApnsTeamID,
		ApnsTopic:        defs.ApnsTopic,
		ApnsSandbox:      defs.ApnsSandbox,
	}

	if err := server.Start(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "server error: %v\n", err)
		os.Exit(1)
	}
}

// Stop reads the PID file written by launchBackground, sends SIGTERM to the
// server process, and removes the PID file.
func Stop() {
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
	fmt.Printf("idtrack %s server stopped (pid %d)\n", record.Version, record.PID)
}

// Restart stops the currently running server and immediately relaunches it
// with the same command-line arguments recorded in the PID file. It waits for
// the old process to exit before starting the new one so the port is free.
func Restart() {
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

	fmt.Printf("idtrack %s server stopped (pid %d)\n", record.Version, record.PID)

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

	launchBackground(record.Args, record.Path)
}

// launchBackground re-executes this binary as a detached background process.
// It prevents duplicate servers by checking the PID file for a running process,
// then redirects child stdout/stderr to the log file and writes the child's PID.
func launchBackground(serveArgs []string, urlPath string) {
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

	// os.Executable returns the path of the currently running binary so we
	// can re-exec it without depending on PATH.
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

	// Open in append mode so repeated server restarts accumulate logs rather
	// than overwriting them.
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
	// parent's process group so the child survives terminal close.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	if err := cmd.Start(); err != nil {
		logFile.Close()
		fmt.Fprintf(os.Stderr, "error starting server: %v\n", err)
		os.Exit(1)
	}

	logFile.Close() // parent no longer needs the file; child inherited its own fd

	// Record the child's PID and serve args so Stop can find the process and
	// Restart can relaunch with the same flags.
	record := pidRecord{PID: cmd.Process.Pid, Path: urlPath, Version: BuildVersion, Args: serveArgs}
	if pidData, err := json.Marshal(record); err == nil {
		if err := os.WriteFile(pidFile, pidData, 0600); err != nil {
			fmt.Fprintf(os.Stderr, "cannot write pid file: %v\n", err)
		}
	}

	fmt.Printf("idtrack %s server started (pid %d) at %s\n", BuildVersion, cmd.Process.Pid, urlPath)
	fmt.Printf("log: %s\n", logPath)
}
