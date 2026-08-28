//go:build windows

package process

import "time"

// The Unix-only regression provides the resistant group descendant. Keep the
// shared test helper buildable on Windows, where Job Objects own the tree.
func runResistantTreeHelper() { time.Sleep(10 * time.Second) }
