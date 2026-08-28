// Package tui implements hostd's interactive, non-AI operator console.
package tui

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"
)

type ProgramRunner func(tea.Model, ...tea.ProgramOption) (tea.Model, error)

// Config wires the terminal model to a controller client and protected stores.
// Client can be supplied directly for tests; production integrations normally
// provide both SessionStoreFactory and ClientFactory.
type Config struct {
	Endpoint            string
	Client              Client
	ClientFactory       ClientFactory
	SessionStoreFactory SessionStoreFactory
	HistoryStoreFactory HistoryStoreFactory
	HistoryPath         string
	ProgramRunner       ProgramRunner
}

// Run starts a full-screen alternate-buffer operator console.
func Run(ctx context.Context, cfg Config) error {
	if ctx == nil {
		return errors.New("tui context is required")
	}
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
	history, err := buildHistoryStore(cfg)
	if err != nil {
		return err
	}
	endpoint := cfg.Endpoint
	if endpoint == "" {
		endpoint = "http://127.0.0.1:7345"
	}
	model := NewModel(ctx, client, history, endpoint)
	runner := cfg.ProgramRunner
	if runner == nil {
		runner = func(model tea.Model, options ...tea.ProgramOption) (tea.Model, error) {
			return tea.NewProgram(model, options...).Run()
		}
	}
	_, err = runner(model, tea.WithContext(ctx), tea.WithAltScreen(), tea.WithMouseCellMotion())
	return err
}

func buildHistoryStore(cfg Config) (HistoryStore, error) {
	if cfg.HistoryStoreFactory != nil {
		return cfg.HistoryStoreFactory()
	}
	path := cfg.HistoryPath
	if path == "" {
		root, err := os.UserConfigDir()
		if err != nil {
			return nil, err
		}
		path = filepath.Join(root, "hostd", "tui-history.json")
	}
	return NewProtectedHistoryStore(path), nil
}
