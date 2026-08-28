package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/hostd/hostd/internal/controllerclient"
	"github.com/hostd/hostd/internal/tui"
)

type tuiLaunchOptions struct{ endpoint, sessionFile, historyFile string }
type hostdRunners struct {
	interactive func() bool
	runUI       func(tuiLaunchOptions) error
	runServer   func([]string) int
	stdout      io.Writer
	stderr      io.Writer
}

func main() {
	os.Exit(runHostd(os.Args[1:], hostdRunners{interactive: interactiveTerminal, runUI: runTUI, runServer: runServer, stdout: os.Stdout, stderr: os.Stderr}))
}

func runHostd(args []string, runners hostdRunners) int {
	if runners.interactive == nil || runners.runUI == nil || runners.runServer == nil {
		fmt.Fprintln(runners.stderr, "hostd dispatch is not configured")
		return 1
	}
	invocation, err := classifyHostdInvocation(args)
	if err != nil {
		fmt.Fprintln(runners.stderr, err)
		return 2
	}
	if invocation.mode == hostdModeServe {
		if invocation.legacyServerArgs {
			fmt.Fprintln(runners.stderr, "warning: invoking hostd with daemon flags is deprecated; use hostd serve ...")
		}
		return runners.runServer(invocation.args)
	}
	if len(args) == 0 && !runners.interactive() {
		fmt.Fprintln(runners.stderr, "hostd requires an interactive terminal for the operator console; use hostd serve to run the daemon")
		return 2
	}
	options, help, err := parseTUIOptions(invocation.args)
	if err != nil {
		fmt.Fprintln(runners.stderr, err)
		return 2
	}
	if help != "" {
		fmt.Fprint(runners.stdout, help)
		return 0
	}
	if err := runners.runUI(options); err != nil {
		fmt.Fprintln(runners.stderr, err)
		return 1
	}
	return 0
}

func parseTUIOptions(args []string) (tuiLaunchOptions, string, error) {
	options := tuiLaunchOptions{endpoint: "http://127.0.0.1:7345", sessionFile: controllerclient.DefaultSessionFile()}
	flags := flag.NewFlagSet("hostd ui", flag.ContinueOnError)
	var help strings.Builder
	flags.SetOutput(&help)
	flags.StringVar(&options.endpoint, "endpoint", options.endpoint, "controller endpoint")
	flags.StringVar(&options.sessionFile, "session-file", options.sessionFile, "protected controller session file")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return options, help.String(), nil
		}
		return options, "", fmt.Errorf("hostd ui: %w", err)
	}
	if flags.NArg() != 0 {
		return options, "", errors.New("hostd ui does not accept positional arguments")
	}
	options.historyFile = filepath.Join(filepath.Dir(options.sessionFile), "hostd-tui-history.json")
	return options, "", nil
}
func interactiveTerminal() bool {
	stdin, inputErr := os.Stdin.Stat()
	stdout, outputErr := os.Stdout.Stat()
	return inputErr == nil && outputErr == nil && stdin.Mode()&os.ModeCharDevice != 0 && stdout.Mode()&os.ModeCharDevice != 0
}
func runTUI(options tuiLaunchOptions) error {
	return tui.Run(context.Background(), tui.Config{
		Endpoint: options.endpoint,
		SessionStoreFactory: func() (tui.SessionStore, error) {
			return newProtectedSessionStore(options.sessionFile), nil
		},
		ClientFactory: func(store tui.SessionStore) (tui.Client, error) {
			return newTUIControllerClient(options.endpoint, store)
		},
		HistoryPath: options.historyFile,
	})
}
