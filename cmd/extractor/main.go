// Command extractor is the service-discovery CLI: it scans a single service repo
// and emits its architecture graph as JSON (see docs/DESIGN.md). The tool is
// paid — a run requires a valid API key (resolved here, validated fail-closed
// before any scan).
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/farhadamjady/service-discovery/internal/exitcode"
	"github.com/farhadamjady/service-discovery/internal/pipeline"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

// run is the testable entry point: args in, streams in, exit code out (no
// os.Exit, no globals), so tests can drive the whole CLI and assert output.
func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("extractor", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		root        = fs.String("root", ".", "repository root to scan")
		apiKey      = fs.String("api-key", "", "API key (overrides EKG_API_KEY and config file)")
		configFile  = fs.String("config", "", "path to config file")
		profiles    = fs.String("profiles", "", "comma-separated active Spring profiles")
		environment = fs.String("environment", "", "deploy overlay to resolve (e.g. staging)")
		out         = fs.String("out", "-", "output path for the service JSON, or - for stdout")
		apiURL      = fs.String("api-url", "", "backend base URL for key validation + submit; empty runs local/dev")
		dryRun      = fs.Bool("dry-run", false, "produce JSON but do not submit")
	)
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return int(exitcode.OK)
		}
		return int(exitcode.Runtime) // flag already printed the usage error
	}

	// Key precedence: --api-key > EKG_API_KEY > config file. Never logged. An
	// empty result is not an error here — the auth gate reports the missing key
	// (exit 10) so the message lives in one place.
	key, err := resolveKey(*apiKey, os.Getenv("EKG_API_KEY"), *configFile)
	if err != nil {
		fmt.Fprintln(stderr, "extractor:", err)
		return exitcode.Of(err)
	}

	svc, err := pipeline.Run(context.Background(), pipeline.Options{
		Root:        *root,
		APIKey:      key,
		ConfigFile:  *configFile,
		Profiles:    splitCSV(*profiles),
		Environment: *environment,
		APIURL:      *apiURL,
		DryRun:      *dryRun,
	})
	if err != nil {
		fmt.Fprintln(stderr, "extractor:", err)
		return exitcode.Of(err)
	}

	data, err := pipeline.Marshal(svc)
	if err != nil {
		fmt.Fprintln(stderr, "extractor:", err)
		return int(exitcode.Runtime)
	}
	if err := writeOutput(*out, data, stdout); err != nil {
		fmt.Fprintln(stderr, "extractor:", err)
		return int(exitcode.Runtime)
	}
	return int(exitcode.OK)
}

// resolveKey applies key precedence. The config-file lookup is intentionally
// tiny (a single `api_key = <value>` / `api_key: <value>` line) — the full
// config file is parsed by the config indexer in PR 8; this only needs the key.
func resolveKey(flagKey, envKey, configFile string) (string, error) {
	if flagKey != "" {
		return flagKey, nil
	}
	if envKey != "" {
		return envKey, nil
	}
	if configFile != "" {
		b, err := os.ReadFile(configFile)
		if err != nil {
			return "", exitcode.Wrap(exitcode.Runtime, "read config file", err)
		}
		if k := scanConfigKey(b); k != "" {
			return k, nil
		}
	}
	return "", nil
}

// scanConfigKey extracts an api_key value from a config file, tolerating both
// `api_key = v` and `api_key: v`, ignoring surrounding quotes and comments.
func scanConfigKey(b []byte) string {
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		for _, sep := range []string{"=", ":"} {
			if k, v, ok := strings.Cut(line, sep); ok && strings.TrimSpace(k) == "api_key" {
				return strings.Trim(strings.TrimSpace(v), `"'`)
			}
		}
	}
	return ""
}

func splitCSV(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func writeOutput(path string, data []byte, stdout io.Writer) error {
	if path == "-" || path == "" {
		_, err := stdout.Write(data)
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
