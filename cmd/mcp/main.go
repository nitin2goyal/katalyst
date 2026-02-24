package main

import (
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/koptimizer/koptimizer/internal/mcp"
)

func main() {
	apiURL := flag.String("api-url", "http://localhost:8080", "Base URL of the KOptimizer REST API")
	flag.Parse()

	// All informational output goes to stderr so stdout stays clean for JSON-RPC.
	logger := log.New(os.Stderr, "[koptimizer-mcp] ", log.LstdFlags)
	logger.Printf("starting MCP server, API URL: %s", *apiURL)

	server := mcp.NewMCPServer(*apiURL)
	if err := server.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		os.Exit(1)
	}
}
