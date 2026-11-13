package proxy

import (
	"net"
	"strings"
)

// exactRank is the rank of an exact hostname; a wildcard ranks by its length
// below it, and no hostname ranks lowest.
const exactRank = 1 << 20

// hostMatches reports whether name matches pattern, an exact name, *.<name> or
// the empty pattern that matches every name. name is lower case.
func hostMatches(pattern, name string) bool {
	switch {
	case pattern == "":
		return true
	case strings.HasPrefix(pattern, "*."):
		suffix := pattern[1:]
		return len(name) > len(suffix) && strings.HasSuffix(name, suffix)
	}
	return pattern == name
}

// hostRank orders the patterns that match one name: exact, then the longer
// wildcard, then the shorter, then no hostname.
func hostRank(pattern string) int {
	switch {
	case pattern == "":
		return 0
	case strings.HasPrefix(pattern, "*."):
		return len(pattern)
	}
	return exactRank
}

// intersect returns the pattern that matches exactly the names both a and b
// match, when there is one.
func intersect(a, b string) (string, bool) {
	wa, wb := strings.HasPrefix(a, "*."), strings.HasPrefix(b, "*.")
	switch {
	case a == "":
		return b, true
	case b == "" || a == b:
		return a, true
	case wa && wb:
		if strings.HasSuffix(a[1:], b[1:]) {
			return a, true
		}
		if strings.HasSuffix(b[1:], a[1:]) {
			return b, true
		}
	case wa:
		if strings.HasSuffix(b, a[1:]) {
			return b, true
		}
	case wb:
		if strings.HasSuffix(a, b[1:]) {
			return a, true
		}
	}
	return "", false
}

// hostOf returns the name of a Host header without its port, in lower case.
func hostOf(hostport string) string {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	return strings.ToLower(strings.TrimSuffix(host, "."))
}

// trimPrefix returns a path prefix without its trailing slash, "/" staying as
// it is, so that a prefix matches on path elements.
func trimPrefix(p string) string {
	if p == "/" {
		return p
	}
	return strings.TrimSuffix(p, "/")
}

// prefixMatches reports whether prefix, as trimPrefix returns it, matches
// path on path elements: /abc matches /abc and /abc/d and not /abcd.
func prefixMatches(prefix, path string) bool {
	return prefix == "/" || path == prefix || strings.HasPrefix(path, prefix+"/")
}

// replacePrefix returns path with its matched prefix replaced by replacement.
func replacePrefix(path, prefix, replacement string) string {
	rest := path
	if prefix != "/" {
		rest = path[len(prefix):]
	}
	if out := strings.TrimSuffix(replacement, "/") + rest; out != "" {
		return out
	}
	return "/"
}
