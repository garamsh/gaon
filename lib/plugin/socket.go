package plugin

import (
	"fmt"
	"path/filepath"
	"regexp"
)

// DefaultDir is the socket directory a plugin and the agent use when none is
// given.
const DefaultDir = "/run/gaon/plugins"

// Kind is what a plugin decides.
type Kind string

// The kinds of plugin.
const (
	KindNetwork Kind = "network"
	KindStorage Kind = "storage"
)

var namePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// SocketPath is the Unix socket of the plugin called name of the given kind:
// <dir>/<kind>-<name>.sock. A name is lower-case letters, digits and hyphens,
// beginning and ending with a letter or digit, so it cannot leave dir.
func SocketPath(dir string, kind Kind, name string) (string, error) {
	if dir == "" {
		return "", fmt.Errorf("plugin directory is empty")
	}
	if !namePattern.MatchString(name) {
		return "", fmt.Errorf("%s plugin name %q is not lower-case letters, digits and hyphens", kind, name)
	}
	return filepath.Join(dir, string(kind)+"-"+name+".sock"), nil
}
