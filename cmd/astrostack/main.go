// Command astrostack is the AstroStack engine: a CLI for inspecting and processing
// astrophotography capture directories, and the HTTP API server.
//
// Usage:
//
//	astrostack inspect <dir>          classify a capture folder and print the inventory
//	astrostack process <dir> [flags]  run the full auto pipeline
//	astrostack video   <file> [flags] process a lunar/planetary video
//	astrostack serve                  run the HTTP API server
//	astrostack device                 run the device server (camera / filter wheel / mount)
//	astrostack mount <sub>            diagnose, probe or soak-test the hand-controller link
//	astrostack migrate                apply database migrations
package main

import (
	"fmt"
	"github.com/verove-jordan/astronomy/internal/buildinfo"
	"os"
)

// version reports the stamped build identity (see internal/buildinfo; "dev" for a bare `go run`).
var version = buildinfo.String()

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
	case "device":
		err = runDevice(os.Args[2:])
	case "mount":
		err = runMount(os.Args[2:])
	case "migrate":
		err = runMigrate(os.Args[2:])
	case "lightpollution-atlas":
		err = runLightPollutionAtlas(os.Args[2:])
	case "skymap-data":
		err = runSkymapData(os.Args[2:])
	case "deepstars-data":
		err = runDeepstarsData(os.Args[2:])
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
  astrostack device                  run the device server (camera / filter wheel / mount)
  astrostack mount <sub>             hand-controller link: doctor | probe | soak
  astrostack migrate                 apply database migrations
  astrostack lightpollution-atlas    build the offline light-pollution atlas (djlorenz model)
  astrostack skymap-data             build the frontend star + constellation dataset (skymap.json)
  astrostack deepstars-data          build the embedded deep star catalogue (internal/deepstars)
  astrostack version                 print the version
`)
}
