//go:generate go run . -root ../../..

// Command gen publishes the provider dialect catalog as reviewable artifacts.
//
// The catalog is a Go table because behaviour must come from code, but a table nobody can
// read is how "which vendor needs which header" creeps back into call sites as string
// comparisons. The rendering lives in internal/provider (PublishJSON / PublishMarkdown) so
// the committed files can be compared byte for byte, here and in the test suite.
//
// Run from the repository root: go run ./internal/provider/gen -root .
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"cyberstrike-ai/internal/provider"
)

const (
	goldenPath = "internal/provider/testdata/provider-catalog.golden.json"
	docsPath   = "docs/zh-CN/provider-catalog.md"
)

func main() {
	root := flag.String("root", ".", "repository root")
	flag.Parse()
	if err := run(*root); err != nil {
		fmt.Fprintln(os.Stderr, "provider gen:", err)
		os.Exit(1)
	}
}

func run(root string) error {
	published := provider.Publish()
	// A catalog that regressed to a row or two would still render a plausible document,
	// so refuse to publish rather than write a silently short one. Measured: 6 vendors.
	if len(published.Rows) < 5 {
		return fmt.Errorf("catalog has %d vendors; expected at least 5 (the table has 6 today) - the table or the scan is broken", len(published.Rows))
	}
	for _, row := range published.Rows {
		if row.Endpoint == "" || row.Vendor == "" {
			return fmt.Errorf("vendor %q published without an endpoint", row.Vendor)
		}
	}
	jsonBytes, err := provider.PublishJSON()
	if err != nil {
		return err
	}
	if err := write(root, goldenPath, jsonBytes); err != nil {
		return err
	}
	if err := write(root, docsPath, provider.PublishMarkdown()); err != nil {
		return err
	}
	fmt.Printf("provider catalog: %d vendors\n", len(published.Rows))
	return nil
}

func write(root, rel string, data []byte) error {
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
