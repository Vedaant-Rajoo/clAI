package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"codeberg.org/newedia/clai/internal/acceptance"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("acceptance-check", flag.ContinueOnError)
	flags.SetOutput(stderr)
	specPath := flags.String("spec", ".local/SPECIFICATION.md", "path to the normative specification")
	manifestPath := flags.String("manifest", ".local/acceptance-manifest.yaml", "path to acceptance-manifest/v1")
	artifactsPath := flags.String("artifacts", ".local/artifacts.yaml", "path to artifact-registry/v1")
	runCase := flags.String("run-case", "", "run one automated acceptance-case ID after validation")
	runAll := flags.Bool("run-all-automated", false, "run every automated acceptance case after validation")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "acceptance-check: unexpected positional arguments")
		return 2
	}
	if *runCase != "" && *runAll {
		fmt.Fprintln(stderr, "acceptance-check: --run-case and --run-all-automated are mutually exclusive")
		return 2
	}

	ids, manifest, err := acceptance.Load(*specPath, *manifestPath, *artifactsPath)
	if err != nil {
		fmt.Fprintf(stderr, "acceptance-check: %v\n", err)
		return 1
	}
	directory, err := repositoryDirectory(*manifestPath)
	if err != nil {
		fmt.Fprintf(stderr, "acceptance-check: resolve repository directory: %v\n", err)
		return 1
	}

	failures := 0
	if *runCase != "" {
		selected, err := acceptance.FindCase(manifest, *runCase)
		if err != nil {
			fmt.Fprintf(stderr, "acceptance-check: %v\n", err)
			return 1
		}
		if !runSelected(stdout, stderr, directory, selected) {
			failures++
		}
	}
	if *runAll {
		for _, selected := range manifest.Cases {
			if selected.ProofType != "automated" {
				continue
			}
			if !runSelected(stdout, stderr, directory, selected) {
				failures++
			}
		}
	}
	fmt.Fprintf(stdout, "ACCEPTANCE-MANIFEST-VALID requirements=%d cases=%d findings=%d\n", len(ids), len(manifest.Cases), len(manifest.Findings))
	if failures > 0 {
		fmt.Fprintf(stderr, "acceptance-check: %d automated acceptance case(s) failed\n", failures)
		return 1
	}
	return 0
}

func repositoryDirectory(manifestPath string) (string, error) {
	directory, err := filepath.Abs(filepath.Dir(manifestPath))
	if err != nil {
		return "", err
	}
	if filepath.Base(directory) == ".local" {
		directory = filepath.Dir(directory)
	}
	return directory, nil
}

func runSelected(stdout, stderr io.Writer, directory string, selected acceptance.Case) bool {
	result, err := acceptance.RunAutomatedCase(context.Background(), directory, selected)
	if result.Output != "" {
		fmt.Fprint(stdout, result.Output)
	}
	if err != nil {
		fmt.Fprintf(stderr, "acceptance-check: %v\n", err)
		return false
	}
	if result.Skipped {
		fmt.Fprintf(stdout, "ACCEPTANCE-CASE-SKIPPED %s reason=%q\n", selected.ID, result.SkipReason)
	} else {
		fmt.Fprintf(stdout, "ACCEPTANCE-CASE-PASS %s\n", selected.ID)
	}
	return true
}
