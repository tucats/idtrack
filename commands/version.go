package commands

import "fmt"

// Version prints the version string. When BuildTime is set (injected by the
// build script via main.go) the output includes the build timestamp.
func Version() {
	if BuildTime != "" {
		fmt.Printf("idtrack version %s (built %s)\n", BuildVersion, BuildTime)
	} else {
		fmt.Printf("idtrack version %s\n", BuildVersion)
	}
}
