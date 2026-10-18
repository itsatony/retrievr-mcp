package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/itsatony/retrievr-mcp/v2/pkg/retrievr"
)

const usageCatalogue = `retrievr-cli catalogue — print the static source catalogue as JSON.

Needs no config file, credentials or network: it describes every built-in
source (category, residency, requires_api_key, supports_get, intents, ...).
'make catalogue' writes it to docs/source-catalogue.json.

Usage:
  retrievr-cli catalogue
`

// runCatalogue writes retrievr.CatalogueJSON() to w. It runs BEFORE any
// config is loaded — the catalogue is configuration-independent by contract.
func runCatalogue(w io.Writer, args []string) int {
	fs := flag.NewFlagSet(subcommandCatalogue, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() { fmt.Fprint(os.Stderr, usageCatalogue) }
	if err := fs.Parse(args); err != nil {
		return exitCodeUsage
	}
	if fs.NArg() > 0 {
		fmt.Fprint(os.Stderr, usageCatalogue)
		return exitCodeUsage
	}
	b, err := retrievr.CatalogueJSON()
	if err != nil {
		fmt.Fprintf(os.Stderr, "retrievr-cli: render catalogue: %v\n", err)
		return exitCodeError
	}
	if _, err := w.Write(b); err != nil {
		fmt.Fprintf(os.Stderr, "retrievr-cli: write catalogue: %v\n", err)
		return exitCodeError
	}
	return exitCodeSuccess
}
