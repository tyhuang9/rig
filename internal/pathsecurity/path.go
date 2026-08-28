// Package pathsecurity contains operating-system-independent path syntax
// checks that must run before a path reaches filesystem APIs.
package pathsecurity

import "strings"

// RejectWindowsNamespace rejects Windows object-manager and network namespace
// paths even when hostd is running on a non-Windows controller. This keeps
// policy decisions independent of the controller's filepath semantics.
func RejectWindowsNamespace(path string) bool {
	if path == "" || strings.IndexByte(path, 0) >= 0 {
		return path != ""
	}

	normalized := strings.ToLower(strings.ReplaceAll(path, "/", `\`))
	if strings.HasPrefix(normalized, `npipe:`) || strings.HasPrefix(normalized, `\\`) {
		return true
	}
	for _, prefix := range []string{
		`\??\`,
		`\device\`,
		`\globalroot\`,
	} {
		if strings.HasPrefix(normalized, prefix) {
			return true
		}
	}
	return false
}
