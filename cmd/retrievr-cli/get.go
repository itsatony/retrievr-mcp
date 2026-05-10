package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/itsatony/retrievr-mcp/pkg/retrievr"
)

const (
	flagNameInclude = "include"

	usageGet = `retrievr-cli get — fetch a single publication by prefixed ID.

Usage:
  retrievr-cli get [flags] <prefixed-id>

Flags:
  --include <fields>  Comma-separated list of: abstract,full_text,references,
                      citations,related,metadata. Honored by sources that
                      support the requested fields; others are silently ignored.
  --format <name>     native (default) | json | xml | markdown | bibtex.
                      bibtex generates BibTeX server-side from the canonical
                      Publication shape.

Example:
  retrievr-cli get arxiv:2401.12345
  retrievr-cli get --include=abstract,citations --format=bibtex openalex:W4366341216
`
)

func runGet(ctx context.Context, client *retrievr.Client, args []string) int {
	fs := flag.NewFlagSet(subcommandGet, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() { fmt.Fprint(os.Stderr, usageGet) }

	includeCSV := fs.String(flagNameInclude, "", "")
	format := fs.String(flagNameFormat, string(retrievr.FormatNative), "")

	if err := fs.Parse(args); err != nil {
		return exitCodeUsage
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fs.Usage()
		return exitCodeUsage
	}
	prefixedID := rest[0]

	include := parseIncludeFields(*includeCSV)

	pub, err := client.Get(ctx, prefixedID, include, retrievr.ContentFormat(*format))
	if err != nil {
		fmt.Fprintf(os.Stderr, "retrievr-cli: get failed: %v\n", err)
		return exitCodeError
	}

	if err := writeJSON(os.Stdout, pub); err != nil {
		fmt.Fprintf(os.Stderr, "retrievr-cli: encode result: %v\n", err)
		return exitCodeError
	}
	return exitCodeSuccess
}

func parseIncludeFields(csv string) []retrievr.IncludeField {
	if csv == "" {
		return nil
	}
	parts := strings.Split(csv, ",")
	out := make([]retrievr.IncludeField, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, retrievr.IncludeField(p))
		}
	}
	return out
}
