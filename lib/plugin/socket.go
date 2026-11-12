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

// MaxSocketPathLen is the longest path a Unix socket can be bound to: the 108
// bytes of sun_path on Linux, less the terminating NUL.
const MaxSocketPathLen = 107

var namePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// SocketPath is the Unix socket of the plugin called name of the given kind:
// <dir>/<kind>-<name>.sock. A name is lower-case letters, digits and hyphens,
// beginning and ending with a letter or digit, so it cannot leave dir. A path
// longer than MaxSocketPathLen is refused.
func SocketPath(dir string, kind Kind, name string) (string, error) {
	if dir == "" {
		return "", fmt.Errorf("plugin directory is empty")
	}
	if !namePattern.MatchString(name) {
		return "", fmt.Errorf("%s plugin name %q is not lower-case letters, digits and hyphens", kind, name)
	}
	path := filepath.Join(dir, string(kind)+"-"+name+".sock")
	if len(path) > MaxSocketPathLen {
		return "", fmt.Errorf("socket path %s is %d bytes, over the %d a Unix socket path can hold", path, len(path), MaxSocketPathLen)
	}
	return path, nil
}
