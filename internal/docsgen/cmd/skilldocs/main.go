package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/Searchie-Inc/mio-cli/internal/catalog"
	"github.com/Searchie-Inc/mio-cli/internal/docsgen"
)

func main() {
	path := flag.String("file", "cmd/skills/content/mio-skill.md", "Path to the skill markdown to regenerate.")
	check := flag.Bool("check", false, "Report whether the file is up to date; write nothing.")
	flag.Parse()

	if err := run(*path, *check); err != nil {
		fmt.Fprintf(os.Stderr, "skilldocs: %v\n", err)
		os.Exit(1)
	}
}

func run(path string, check bool) error {
	cat, err := catalog.Load()
	if err != nil {
		return fmt.Errorf("load embedded catalog: %w", err)
	}
	blocks, err := docsgen.Render(cat)
	if err != nil {
		return err
	}
	current, err := os.ReadFile(path) //nolint:gosec
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	next, err := docsgen.Apply(string(current), blocks)
	if err != nil {
		return err
	}
	if next == string(current) {
		fmt.Fprintf(os.Stderr, "skilldocs: %s is up to date (catalog %s)\n", path, cat.Meta.CatalogVersion)
		return nil
	}
	if check {
		return fmt.Errorf("stale: %s does not match the embedded catalog (%s); run go generate ./... to refresh it", path, cat.Meta.CatalogVersion)
	}
	if err := os.WriteFile(path, []byte(next), 0o644); err != nil { //nolint:gosec
		return fmt.Errorf("write %s: %w", path, err)
	}
	fmt.Fprintf(os.Stderr, "skilldocs: regenerated %s from catalog %s\n", path, cat.Meta.CatalogVersion)
	return nil
}
