package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/itsatony/retrievr-mcp/v2/pkg/retrievr"
)

const usageSources = `retrievr-cli sources — list available source plugins.

Usage:
  retrievr-cli sources [flags]

Flags:
  --format <name>   table (default) or json.
`

func runSources(ctx context.Context, client *retrievr.Client, args []string) int {
	fs := flag.NewFlagSet(subcommandSources, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() { fmt.Fprint(os.Stderr, usageSources) }

	format := fs.String(flagNameFormat, formatTable, "")
	if err := fs.Parse(args); err != nil {
		return exitCodeUsage
	}

	infos := client.ListSources(ctx)
	sort.Slice(infos, func(i, j int) bool { return infos[i].ID < infos[j].ID })

	switch *format {
	case formatJSON:
		if err := writeJSON(os.Stdout, infos); err != nil {
			fmt.Fprintf(os.Stderr, "retrievr-cli: encode sources: %v\n", err)
			return exitCodeError
		}
	case formatTable:
		writeSourcesTable(os.Stdout, infos)
	default:
		fmt.Fprintf(os.Stderr, "retrievr-cli: invalid --format %q\n", *format)
		return exitCodeUsage
	}
	return exitCodeSuccess
}

func writeSourcesTable(w *os.File, infos []retrievr.SourceInfo) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	defer func() { _ = tw.Flush() }()

	fmt.Fprintf(tw, "ID\tNAME\tENABLED\tAUTH\tCONTENT\tRATE/s\n")
	for _, s := range infos {
		fmt.Fprintf(tw, "%s\t%s\t%t\t%s\t%s\t%.1f\n",
			s.ID,
			s.Name,
			s.Enabled,
			authMarker(s.AcceptsCredentials),
			joinContentTypes(s.ContentTypes),
			s.RateLimit.RequestsPerSecond,
		)
	}
}

func authMarker(acceptsCreds bool) string {
	if acceptsCreds {
		return "key"
	}
	return "anon"
}

func joinContentTypes(cts []retrievr.ContentType) string {
	parts := make([]string, 0, len(cts))
	for _, ct := range cts {
		parts = append(parts, string(ct))
	}
	return strings.Join(parts, "|")
}
