package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/hostd/hostd/internal/apicontract"
	"github.com/hostd/hostd/internal/auth"
	"github.com/hostd/hostd/internal/controllerclient"
	"github.com/hostd/hostd/internal/secretfile"
	"github.com/spf13/cobra"
)

type sessionCredentials = controllerclient.Session

const maxSessionFileBytes = 64 << 10
const sessionFilePurpose = "hostctl-session"

type commandApp struct {
	endpoint     string
	sessionFile  string
	sessionStdin bool
	input        io.Reader
	output       io.Writer
	httpClient   *http.Client
}

func main() {
	if err := execute(os.Args[1:], os.Stdin, os.Stdout, &http.Client{Timeout: 10 * time.Second}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func execute(args []string, input io.Reader, output io.Writer, client *http.Client) error {
	app := &commandApp{endpoint: "http://127.0.0.1:7345", sessionFile: defaultSessionFile(), input: input, output: output, httpClient: client}
	root := &cobra.Command{Use: "hostctl", Short: "hostd local control-plane client", SilenceUsage: true, SilenceErrors: true}
	root.SetArgs(args)
	root.PersistentFlags().StringVar(&app.endpoint, "endpoint", app.endpoint, "hostd endpoint")
	root.PersistentFlags().StringVar(&app.sessionFile, "session-file", app.sessionFile, "protected hostctl session JSON file")
	root.PersistentFlags().BoolVar(&app.sessionStdin, "session-stdin", false, "read session JSON from standard input instead of a file")

	credentialsStdin := false
	login := &cobra.Command{Use: "login", Short: "create a protected local CLI session", Args: cobra.NoArgs, RunE: func(_ *cobra.Command, _ []string) error {
		if !credentialsStdin {
			return errors.New("login requires --credentials-stdin so passphrases never appear in process arguments")
		}
		var credentials struct {
			Username   string `json:"username"`
			Passphrase string `json:"passphrase"`
		}
		if err := decodeLimited(app.input, &credentials); err != nil {
			return fmt.Errorf("read credentials from stdin: %w", err)
		}
		if credentials.Username == "" || credentials.Passphrase == "" {
			return errors.New("stdin credentials require username and passphrase")
		}
		client, err := app.controllerClient()
		if err != nil {
			return err
		}
		_, session, err := client.Login(context.Background(), apicontract.LoginRequest{Username: credentials.Username, Passphrase: credentials.Passphrase})
		if err != nil {
			return err
		}
		if err := writeSessionFile(app.sessionFile, session); err != nil {
			return err
		}
		_, err = fmt.Fprintf(app.output, "Session saved to %s\n", app.sessionFile)
		return err
	}}
	login.Flags().BoolVar(&credentialsStdin, "credentials-stdin", false, "read username/passphrase JSON from standard input")
	root.AddCommand(login)

	var bootstrapTokenFile string
	bootstrapToken := &cobra.Command{Use: "bootstrap-token", Short: "write a protected local bootstrap token to stdout", Args: cobra.NoArgs, RunE: func(_ *cobra.Command, _ []string) error {
		if bootstrapTokenFile == "" {
			return errors.New("bootstrap-token requires --file")
		}
		token, err := secretfile.Read(bootstrapTokenFile, auth.BootstrapSecretPurpose)
		if err != nil {
			return fmt.Errorf("read bootstrap token: %w", err)
		}
		defer clear(token)
		_, err = fmt.Fprintln(app.output, string(token))
		return err
	}}
	bootstrapToken.Flags().StringVar(&bootstrapTokenFile, "file", "", "protected bootstrap token file printed by hostd")
	root.AddCommand(bootstrapToken)

	for name := range map[string]struct{}{"status": {}, "doctor": {}} {
		name := name
		root.AddCommand(&cobra.Command{Use: name, Args: cobra.NoArgs, RunE: func(_ *cobra.Command, _ []string) error {
			session, err := app.loadSession()
			if err != nil {
				return err
			}
			client, err := app.controllerClient()
			if err != nil {
				return err
			}
			if name == "status" {
				value, err := client.Status(context.Background(), session)
				if err != nil {
					return err
				}
				return app.printJSON(value)
			}
			value, err := client.Doctor(context.Background(), session)
			if err != nil {
				return err
			}
			return app.printJSON(value)
		}})
	}
	jobsCommand := &cobra.Command{Use: "jobs", Short: "manage durable jobs"}
	jobsCommand.AddCommand(&cobra.Command{Use: "cancel JOB_ID", Args: cobra.ExactArgs(1), RunE: func(_ *cobra.Command, args []string) error {
		session, err := app.loadSession()
		if err != nil {
			return err
		}
		client, err := app.controllerClient()
		if err != nil {
			return err
		}
		value, err := client.Cancel(context.Background(), &session, args[0])
		if err != nil {
			return err
		}
		return app.printJSON(value)
	}})
	root.AddCommand(jobsCommand)
	return root.Execute()
}

func (app *commandApp) printJSON(payload any) error {
	encoded, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(app.output, string(encoded))
	return err
}

func (app *commandApp) controllerClient() (*controllerclient.Client, error) {
	return controllerclient.New(controllerclient.Options{Endpoint: app.endpoint, HTTPClient: app.httpClient})
}

func (app *commandApp) loadSession() (sessionCredentials, error) {
	var session sessionCredentials
	if app.sessionStdin {
		if err := decodeLimited(app.input, &session); err != nil {
			return session, fmt.Errorf("read session from stdin: %w", err)
		}
	} else {
		loaded, err := controllerclient.ReadSessionFile(app.sessionFile)
		if err != nil {
			return session, err
		}
		session = loaded
	}
	return session, nil
}

func decodeLimited(reader io.Reader, target any) error {
	decoder := json.NewDecoder(io.LimitReader(reader, maxSessionFileBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return errors.New("input must contain one JSON object")
	}
	return nil
}

func encodeSessionFile(session sessionCredentials) ([]byte, error) {
	var plaintext bytes.Buffer
	if err := json.NewEncoder(&plaintext).Encode(session); err != nil {
		return nil, err
	}
	return append([]byte(nil), plaintext.Bytes()...), nil
}

func defaultSessionFile() string {
	return controllerclient.DefaultSessionFile()
}

func writeSessionFile(path string, session sessionCredentials) error {
	return controllerclient.WriteSessionFile(path, session)
}
