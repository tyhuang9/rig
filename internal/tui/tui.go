// Package tui implements hostd's interactive, non-AI application switchboard.
package tui

import (
	"context"
	"errors"

	tea "github.com/charmbracelet/bubbletea"
)

type ProgramRunner func(tea.Model, ...tea.ProgramOption) (tea.Model, error)
type URLOpener func(context.Context, string) error

// Config wires the terminal model to a controller client and protected session
// store. OpenURL receives only the validated controller origin.
type Config struct {
	Endpoint            string
	Client              Client
	ClientFactory       ClientFactory
	SessionStoreFactory SessionStoreFactory
	ProgramRunner       ProgramRunner
	OpenURL             URLOpener
	Accessible          bool
}

// Run starts the operator switchboard. Accessible mode remains in the primary
// terminal buffer; normal mode uses the alternate buffer. Neither reports mouse
// events.
func Run(ctx context.Context, cfg Config) error {
	if ctx == nil {
		return errors.New("tui context is required")
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	client := cfg.Client
	if client == nil {
		if cfg.ClientFactory == nil || cfg.SessionStoreFactory == nil {
			return errors.New("tui client or client/session factories are required")
		}
		store, err := cfg.SessionStoreFactory()
		if err != nil {
			return err
		}
		client, err = cfg.ClientFactory(store)
		if err != nil {
			return err
		}
	}
	endpoint := cfg.Endpoint
	if endpoint == "" {
		endpoint = "http://127.0.0.1:7345"
	}
	model := NewModel(runCtx, client, endpoint)
	model.accessible = cfg.Accessible
	model.openURL = cfg.OpenURL

	runner := cfg.ProgramRunner
	if runner == nil {
		runner = func(model tea.Model, options ...tea.ProgramOption) (tea.Model, error) {
			return tea.NewProgram(model, options...).Run()
		}
	}
	options := []tea.ProgramOption{tea.WithContext(runCtx)}
	if !cfg.Accessible {
		options = append(options, tea.WithAltScreen())
	}
	_, err := runner(model, options...)
	return err
}
