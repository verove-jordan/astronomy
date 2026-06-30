// Command astrostack is the AstroStack engine: a CLI for inspecting and processing
// astrophotography capture directories, and the HTTP API server.
//
// Usage:
//
//	astrostack inspect <dir>          classify a capture folder and print the inventory
//	astrostack process <dir> [flags]  run the full auto pipeline
//	astrostack video   <file> [flags] process a lunar/planetary video
//	astrostack serve                  run the HTTP API server
//	astrostack migrate                apply database migrations
package main

import (
	"fmt"
	"os"
)

// version is overridden at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	var err error
	switch cmd := os.Args[1]; cmd {
	case "inspect":
		err = runInspect(os.Args[2:])
	case "process":
		err = runProcess(os.Args[2:])
	case "refine":
		err = runRefine(os.Args[2:])
	case "video":
		err = runVideo(os.Args[2:])
	case "serve":
		err = runServe(os.Args[2:])
	case "migrate":
		err = runMigrate(os.Args[2:])
	case "version", "--version", "-v":
		fmt.Println("astrostack", version)
	case "help", "--help", "-h":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "astrostack: unknown command %q\n\n", cmd)
		usage()
		os.Exit(2)
	}

	if err != nil {
		fmt.Fprintln(os.Stderr, "astrostack:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `astrostack — automated astrophotography sorting & stacking

Usage:
  astrostack inspect <dir>           classify a capture folder and print the inventory
  astrostack process <dir> [flags]   run the full auto pipeline
  astrostack refine  <run-dir>       re-run the finish (local AI agent) on an existing run — no re-stack
  astrostack video   <file> [flags]  process a lunar/planetary video
  astrostack serve                   run the HTTP API server
  astrostack migrate                 apply database migrations
  astrostack version                 print the version
`)
}
