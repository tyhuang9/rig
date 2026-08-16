package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/hostd/hostd/internal/auth"
	"github.com/spf13/cobra"
)

type sessionCredentials struct {
	SessionToken string `json:"sessionToken"`
	CSRFToken    string `json:"csrfToken"`
}

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
		body, err := json.Marshal(credentials)
		if err != nil {
			return err
		}
		response, err := app.request(http.MethodPost, "/api/v1/auth/sessions", body, sessionCredentials{})
		if err != nil {
			return err
		}
		defer response.Body.Close()
		var payload struct {
			CSRFToken string `json:"csrfToken"`
		}
		if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
			return fmt.Errorf("decode login response: %w", err)
		}
		var sessionToken string
		for _, cookie := range response.Cookies() {
			if cookie.Name == auth.SessionCookie {
				sessionToken = cookie.Value
			}
		}
		if sessionToken == "" || payload.CSRFToken == "" {
			return errors.New("hostd login response omitted session credentials")
		}
		if err := writeSessionFile(app.sessionFile, sessionCredentials{SessionToken: sessionToken, CSRFToken: payload.CSRFToken}); err != nil {
			return err
		}
		_, err = fmt.Fprintf(app.output, "Session saved to %s\n", app.sessionFile)
		return err
	}}
	login.Flags().BoolVar(&credentialsStdin, "credentials-stdin", false, "read username/passphrase JSON from standard input")
	root.AddCommand(login)

	for name, path := range map[string]string{"status": "/api/v1/system/status", "doctor": "/api/v1/system/doctor"} {
		path := path
		root.AddCommand(&cobra.Command{Use: name, Args: cobra.NoArgs, RunE: func(_ *cobra.Command, _ []string) error {
			session, err := app.loadSession()
			if err != nil {
				return err
			}
			return app.printResponse(http.MethodGet, path, nil, session)
		}})
	}
	jobsCommand := &cobra.Command{Use: "jobs", Short: "manage durable jobs"}
	jobsCommand.AddCommand(&cobra.Command{Use: "cancel JOB_ID", Args: cobra.ExactArgs(1), RunE: func(_ *cobra.Command, args []string) error {
		session, err := app.loadSession()
		if err != nil {
			return err
		}
		return app.printResponse(http.MethodPost, "/api/v1/jobs/"+url.PathEscape(args[0])+"/cancel", nil, session)
	}})
	root.AddCommand(jobsCommand)
	return root.Execute()
}

func (app *commandApp) printResponse(method, path string, body []byte, session sessionCredentials) error {
	response, err := app.request(method, path, body, session)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	var payload any
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return fmt.Errorf("decode hostd response: %w", err)
	}
	encoded, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(app.output, string(encoded))
	return err
}

func (app *commandApp) request(method, path string, body []byte, session sessionCredentials) (*http.Response, error) {
	request, err := http.NewRequest(method, strings.TrimRight(app.endpoint, "/")+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if session.SessionToken != "" {
		request.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: session.SessionToken})
	}
	if method != http.MethodGet && method != http.MethodHead && session.CSRFToken != "" {
		request.Header.Set("X-CSRF-Token", session.CSRFToken)
	}
	response, err := app.httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("hostd request failed: %w", err)
	}
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		return response, nil
	}
	defer response.Body.Close()
	var problem struct {
		Detail string `json:"detail"`
	}
	_ = json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&problem)
	if problem.Detail == "" {
		problem.Detail = http.StatusText(response.StatusCode)
	}
	return nil, fmt.Errorf("hostd returned %s: %s", response.Status, problem.Detail)
}

func (app *commandApp) loadSession() (sessionCredentials, error) {
	var session sessionCredentials
	if app.sessionStdin {
		if err := decodeLimited(app.input, &session); err != nil {
			return session, fmt.Errorf("read session from stdin: %w", err)
		}
	} else {
		info, err := os.Stat(app.sessionFile)
		if err != nil {
			return session, fmt.Errorf("read session file: %w", err)
		}
		if !info.Mode().IsRegular() {
			return session, errors.New("session file must be a regular file")
		}
		if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
			return session, errors.New("session file permissions are too broad; require 0600")
		}
		file, err := os.Open(app.sessionFile)
		if err != nil {
			return session, fmt.Errorf("read session file: %w", err)
		}
		defer file.Close()
		if err := decodeLimited(file, &session); err != nil {
			return session, fmt.Errorf("decode session file: %w", err)
		}
	}
	if session.SessionToken == "" || session.CSRFToken == "" {
		return session, errors.New("session credentials are incomplete")
	}
	return session, nil
}

func decodeLimited(reader io.Reader, target any) error {
	decoder := json.NewDecoder(io.LimitReader(reader, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return errors.New("input must contain one JSON object")
	}
	return nil
}

func defaultSessionFile() string {
	root, err := os.UserConfigDir()
	if err != nil {
		root = "."
	}
	return filepath.Join(root, "hostd", "hostctl-session.json")
}

func writeSessionFile(path string, session sessionCredentials) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create session directory: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".hostctl-session-*")
	if err != nil {
		return fmt.Errorf("create temporary session file: %w", err)
	}
	temporaryPath := temporary.Name()
	keep := false
	defer func() {
		_ = temporary.Close()
		if !keep {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return err
	}
	if err := json.NewEncoder(temporary).Encode(session); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := replaceFile(temporaryPath, path); err != nil {
		return fmt.Errorf("replace session file: %w", err)
	}
	keep = true
	return nil
}
