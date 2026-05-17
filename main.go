// Package main is the CLI entry point for idtrack. It dispatches sub-commands
// to the commands package and owns the two values that are injected at link
// time by the build script: BuildVersion and BuildTime.
package main

import (
	"embed"
	"fmt"
	"io/fs"
	"os"

	"github.com/tucats/idtrack/commands"
)

// BuildVersion and BuildTime are set at link time by the build script with
// -ldflags "-X main.BuildVersion=... -X main.BuildTime=...".
// When you run a plain "go build" without those flags, they keep their default
// values so the binary still works — it just shows "dev" for the version.
var BuildVersion = "dev"
var BuildTime = ""

// embedded holds the contents of the resources/ directory, compiled directly
// into the binary. The //go:embed directive must live alongside the resources/
// directory, which is why it stays in package main rather than commands.
//
//go:embed resources
var embedded embed.FS

func main() {
	// Make build-time values available to the commands package before dispatch.
	commands.BuildVersion = BuildVersion
	commands.BuildTime = BuildTime

	args := os.Args[1:]
	if len(args) == 0 {
		commands.Usage()
		os.Exit(1)
	}

	switch args[0] {
	case "help", "--help", "-h":
		commands.Usage()
		os.Exit(0)
	case "serve", "start", "run":
		commands.Serve(args[1:], fs.FS(embedded))
	case "stop":
		commands.Stop()
	case "restart":
		commands.Restart()
	case "default", "defaults", "config":
		commands.Default(args[1:])
	case "user", "users":
		commands.User(args[1:])
	case "define":
		commands.Define(args[1:])
	case "delete":
		commands.Delete(args[1:])
	case "version", "-v", "--version":
		commands.Version()
	default:
		fmt.Fprintf(os.Stderr, "unknown verb: %s\n", args[0])
		commands.Usage()
		os.Exit(1)
	}
}
