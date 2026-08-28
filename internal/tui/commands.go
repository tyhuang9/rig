package tui

import (
	"fmt"
	"sort"
	"strings"
)

type command struct {
	Name string
	Args []string
	Raw  string
}

type commandSpec struct {
	Usage       string
	Description string
	Confirm     bool
}

var commandSpecs = map[string]commandSpec{
	"/help":          {Usage: "/help", Description: "show commands"},
	"/clear":         {Usage: "/clear", Description: "clear the local transcript"},
	"/history clear": {Usage: "/history clear", Description: "erase protected command history", Confirm: true},
	"/quit":          {Usage: "/quit", Description: "exit the console", Confirm: true},
	"/logout":        {Usage: "/logout", Description: "end the controller session", Confirm: true},
	"/status":        {Usage: "/status", Description: "show controller status"},
	"/doctor":        {Usage: "/doctor", Description: "run controller diagnostics"},
	"/apps":          {Usage: "/apps", Description: "list applications"},
	"/app":           {Usage: "/app", Description: "show the selected application"},
	"/use":           {Usage: "/use <slug-or-id>", Description: "select an application"},
	"/machines":      {Usage: "/machines", Description: "list machines"},
	"/deploy":        {Usage: "/deploy [slug-or-id]", Description: "deploy an application", Confirm: true},
	"/start":         {Usage: "/start [slug-or-id]", Description: "start an application", Confirm: true},
	"/stop":          {Usage: "/stop [slug-or-id]", Description: "stop an application", Confirm: true},
	"/restart":       {Usage: "/restart [slug-or-id]", Description: "restart an application", Confirm: true},
	"/jobs":          {Usage: "/jobs", Description: "list recent jobs"},
	"/job":           {Usage: "/job <id>", Description: "show a job"},
	"/follow":        {Usage: "/follow <id>", Description: "follow job events"},
	"/cancel":        {Usage: "/cancel <id>", Description: "cancel a job", Confirm: true},
	"/resume":        {Usage: "/resume <id>", Description: "resume a paused job", Confirm: true},
}

func parseCommand(raw string) (command, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return command{}, fmt.Errorf("enter a command; /help lists available commands")
	}
	fields := strings.Fields(raw)
	if !strings.HasPrefix(fields[0], "/") {
		return command{}, fmt.Errorf("this console accepts operator commands; try /help")
	}
	name := strings.ToLower(fields[0])
	args := fields[1:]
	if name == "/history" {
		if len(args) != 1 || strings.ToLower(args[0]) != "clear" {
			return command{}, fmt.Errorf("usage: /history clear")
		}
		name = "/history clear"
		args = nil
	}
	spec, ok := commandSpecs[name]
	if !ok {
		return command{}, fmt.Errorf("unknown command %q; try /help", sanitizeAPIText(fields[0]))
	}
	switch name {
	case "/use", "/job", "/follow", "/cancel", "/resume":
		if len(args) != 1 {
			return command{}, fmt.Errorf("usage: %s", spec.Usage)
		}
	case "/deploy", "/start", "/stop", "/restart":
		if len(args) > 1 {
			return command{}, fmt.Errorf("usage: %s", spec.Usage)
		}
	default:
		if len(args) != 0 {
			return command{}, fmt.Errorf("usage: %s", spec.Usage)
		}
	}
	return command{Name: name, Args: args, Raw: raw}, nil
}

func commandSuggestions(value string) []string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return []string{"/status", "/apps", "/jobs", "/help"}
	}
	result := make([]string, 0, len(commandSpecs))
	seen := make(map[string]struct{})
	for key, spec := range commandSpecs {
		if strings.HasPrefix(strings.ToLower(spec.Usage), value) {
			completion := key
			if completion == "/history clear" {
				completion = spec.Usage
			}
			if _, ok := seen[completion]; !ok {
				seen[completion] = struct{}{}
				result = append(result, completion)
			}
		}
	}
	sort.Strings(result)
	if len(result) > 6 {
		result = result[:6]
	}
	return result
}

func helpText() string {
	keys := make([]string, 0, len(commandSpecs))
	for key := range commandSpecs {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, key := range keys {
		spec := commandSpecs[key]
		fmt.Fprintf(&b, "%-23s %s\n", spec.Usage, spec.Description)
	}
	return strings.TrimSuffix(b.String(), "\n")
}

func confirmationText(cmd command, selected string) string {
	switch cmd.Name {
	case "/deploy", "/start", "/stop", "/restart":
		return fmt.Sprintf("Run %s for %s?", cmd.Name, selected)
	case "/cancel", "/resume":
		return fmt.Sprintf("Run %s for job %s?", cmd.Name, cmd.Args[0])
	case "/logout":
		return "End the current controller session?"
	case "/history clear":
		return "Erase all saved command history?"
	case "/quit":
		return "Exit the operator console?"
	default:
		return fmt.Sprintf("Run %s?", cmd.Name)
	}
}
