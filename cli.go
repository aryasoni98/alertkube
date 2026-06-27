package main

import (
	"flag"
	"fmt"
	"io"
	"runtime"

	"alertkube/internal/config"
)

// dispatchSubcommand handles the optional first-positional subcommands
// (`version`, `validate`) that run without a cluster connection, so config can
// be linted in CI/pre-commit and the build provenance inspected without exec-ing
// into a pod. It returns (handled, exitCode): handled=false means no subcommand
// matched and main should fall through to the controller. Kept in its own file,
// taking explicit writers and args, so it is unit-testable without touching
// os.Args/os.Exit.
func dispatchSubcommand(args []string, stdout, stderr io.Writer) (handled bool, exitCode int) {
	if len(args) == 0 {
		return false, 0
	}
	switch args[0] {
	case "version", "--version", "-version":
		return true, runVersion(stdout)
	case "validate":
		return true, runValidate(args[1:], stdout, stderr)
	default:
		return false, 0
	}
}

// runVersion prints the build version plus Go/runtime provenance. The version
// is stamped at build time via -ldflags (see main.version); printing it without
// a cluster connection lets `docker run <img> version` report the image build.
func runVersion(stdout io.Writer) int {
	fmt.Fprintf(stdout, "%s %s\n", appName, version)
	fmt.Fprintf(stdout, "  go:    %s\n", runtime.Version())
	fmt.Fprintf(stdout, "  arch:  %s/%s\n", runtime.GOOS, runtime.GOARCH)
	return 0
}

// runValidate loads and validates a config file using the exact same path as
// startup (config.Load: read file -> apply env defaults -> Validate), so a
// "valid" verdict here guarantees the controller will not reject the config at
// boot. The path comes from `--config`, a positional argument, or the
// ALERTKUBE_CONFIG env var, in that order. Exit 0 = valid, 2 = usage error,
// 1 = invalid config, so CI/pre-commit can gate on the exit code.
func runValidate(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("validate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configFlag := fs.String("config", "", "path to the YAML config to validate")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: alertkube validate [--config <path> | <path>]")
		fmt.Fprintln(stderr, "  Validates a config file the same way the controller does at startup.")
		fmt.Fprintln(stderr, "  Exit code: 0 valid, 1 invalid, 2 usage error.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}

	path := *configFlag
	if path == "" && fs.NArg() > 0 {
		path = fs.Arg(0)
	}
	if path == "" {
		path = envConfigPath()
	}
	if path == "" {
		fmt.Fprintln(stderr, "validate: no config path given (use --config, a positional path, or ALERTKUBE_CONFIG)")
		fs.Usage()
		return 2
	}

	if _, err := config.Load(path); err != nil {
		fmt.Fprintf(stderr, "✗ %s: %v\n", path, err)
		return 1
	}
	fmt.Fprintf(stdout, "✓ %s is valid\n", path)
	return 0
}
