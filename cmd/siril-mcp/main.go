// Command siril-mcp is an MCP server that brokers a host-installed Siril for Claude,
// exposing the AstroStack engine (inspect / grade / calibrate / register / stack /
// run_pipeline) as MCP tools over stdio.
//
// The full implementation lands in milestone M6; this entrypoint is the scaffold.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "siril-mcp: scaffold (full MCP server implemented in M6)")
}
