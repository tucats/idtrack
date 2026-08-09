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
//
// Doc-comment convention: notice that this comment starts with the name of
// the thing it documents ("BuildVersion and BuildTime are set..."). That is
// a Go convention, not just a style preference — tools like `go doc`, godoc.org,
// and IDE hover tooltips pull the comment immediately above an exported
// (capitalized) declaration and display its first sentence as the summary.
// Every exported function, type, and package-level var/const in this project
// follows that pattern: start the comment with the identifier's own name.
var BuildVersion = "dev"
var BuildTime = ""

// embedded holds the contents of the resources/ directory, compiled directly
// into the binary. The //go:embed directive must live alongside the resources/
// directory, which is why it stays in package main rather than commands.
//
//go:embed resources
var embedded embed.FS

// main is the process entry point. It performs no real work itself: it wires
// the link-time build variables into the commands package, then dispatches to
// exactly one commands.* function based on the first command-line argument
// (the "verb", e.g. "serve" or "user"). Everything after the verb (args[1:])
// is passed through unparsed — each commands.* function is responsible for
// parsing its own flags and positional arguments. Several verbs accept more
// than one spelling (e.g. "serve"/"start"/"run") purely as user-friendly
// aliases; they all map to the same underlying function.
func main() {
	// Make build-time values available to the commands package before dispatch.
	commands.BuildVersion = BuildVersion
	commands.BuildTime = BuildTime

	args := os.Args[1:]
	if len(args) == 0 {
		commands.Usage()
		os.Exit(1)
	}

	// args[0] is the verb; args[1:] are that verb's own flags/positional
	// arguments, forwarded unparsed to the matching commands.* function.
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
	case "teams", "team":
		commands.Teams(args[1:])
	case "define":
		commands.Define(args[1:])
	case "ingest":
		commands.Ingest(args[1:])
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
