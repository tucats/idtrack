package main

import (
	"crypto/sha256"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

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
	fmt.Fprintln(os.Stderr, "  idtrack serve [--port n] [--database path]")
	fmt.Fprintln(os.Stderr, "  idtrack user --add username:password [--database path]")
	fmt.Fprintln(os.Stderr, "  idtrack user --delete username [--database path]")
}

func runServe(args []string) {
	defs := loadDefaults()
	port := defs.Port
	database := defs.Database

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

	if database == "" {
		database = "idtrack.db"
	}
	if port == 0 {
		port = 8443
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

func runUser(args []string) {
	var add, del, database string

	for i := 0; i < len(args); i++ {
		switch args[i] {
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

	if add == "" && del == "" {
		fmt.Fprintln(os.Stderr, "must specify --add or --delete")
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

	if add != "" {
		parts := strings.SplitN(add, ":", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			fmt.Fprintln(os.Stderr, "--add requires username:password")
			os.Exit(1)
		}
		username, password := parts[0], parts[1]
		hash := fmt.Sprintf("%x", sha256.Sum256([]byte(password)))
		if err := db.AddUser(d, username, username, hash); err != nil {
			fmt.Fprintf(os.Stderr, "error adding user %q: %v\n", username, err)
			os.Exit(1)
		}
		fmt.Printf("user %q added\n", username)
	}

	if del != "" {
		if err := db.DeleteUser(d, del); err != nil {
			fmt.Fprintf(os.Stderr, "error deleting user %q: %v\n", del, err)
			os.Exit(1)
		}
		fmt.Printf("user %q deleted\n", del)
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
