// Package config reads the gaon CLI's config file of clusters, users and
// contexts, and says which server and credential files its current context
// names.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"go.yaml.in/yaml/v3"
)

// Error is a problem with the config file or with what the command line asks
// of it. Every message names the file.
type Error struct{ msg string }

func (e *Error) Error() string { return e.msg }

func errorf(format string, args ...any) error {
	return &Error{msg: fmt.Sprintf(format, args...)}
}

// File is a parsed config file.
type File struct {
	path string

	CurrentContext string         `yaml:"current-context,omitempty"`
	Clusters       []NamedCluster `yaml:"clusters,omitempty"`
	Users          []NamedUser    `yaml:"users,omitempty"`
	Contexts       []NamedContext `yaml:"contexts,omitempty"`
}

// NamedCluster is a server the CLI can reach.
type NamedCluster struct {
	Name    string  `yaml:"name"`
	Cluster Cluster `yaml:"cluster"`
}

// Cluster is the --server URL and the file of the CA the server's certificate
// must chain to.
type Cluster struct {
	Server               string `yaml:"server"`
	CertificateAuthority string `yaml:"certificate-authority,omitempty"`
}

// NamedUser is a client identity.
type NamedUser struct {
	Name string `yaml:"name"`
	User User   `yaml:"user"`
}

// User is the files of a client certificate and its key.
type User struct {
	ClientCertificate string `yaml:"client-certificate,omitempty"`
	ClientKey         string `yaml:"client-key,omitempty"`
}

// NamedContext pairs a cluster with the user that acts on it.
type NamedContext struct {
	Name    string  `yaml:"name"`
	Context Context `yaml:"context"`
}

// Context names a cluster and, optionally, a user.
type Context struct {
	Cluster string `yaml:"cluster"`
	User    string `yaml:"user,omitempty"`
}

// Selected is what the current context gives the CLI. A field is empty where
// the file gives none. The file paths are absolute where the file is.
type Selected struct {
	Server, CA, Cert, Key string
}

// Load reads and checks the file at path. A file that does not exist is an
// empty File when mustExist is false, and an error naming it otherwise.
func Load(path string, mustExist bool) (*File, error) {
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist) && !mustExist:
		return &File{path: path}, nil
	case errors.Is(err, fs.ErrNotExist):
		return nil, errorf("config file %s does not exist", path)
	case err != nil:
		return nil, errorf("config file %s: %v", path, err)
	}
	f := &File{path: path}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(f); err != nil && !errors.Is(err, io.EOF) {
		return nil, errorf("config file %s: %v", path, err)
	}
	if err := f.checkNames(); err != nil {
		return nil, err
	}
	return f, nil
}

// checkNames refuses a name given twice among clusters, users or contexts.
func (f *File) checkNames() error {
	for _, list := range []struct {
		what  string
		names []string
	}{
		{"cluster", names(f.Clusters, func(c NamedCluster) string { return c.Name })},
		{"user", names(f.Users, func(u NamedUser) string { return u.Name })},
		{"context", names(f.Contexts, func(c NamedContext) string { return c.Name })},
	} {
		seen := map[string]bool{}
		for _, n := range list.names {
			if n == "" {
				return errorf("config file %s: a %s has no name", f.path, list.what)
			}
			if seen[n] {
				return errorf("config file %s: %s %q is defined twice", f.path, list.what, n)
			}
			seen[n] = true
		}
	}
	return nil
}

func names[T any](items []T, name func(T) string) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = name(it)
	}
	return out
}

// Selected resolves the current context. A file with no current context, or
// no file at all, selects nothing and is not an error.
func (f *File) Selected() (Selected, error) {
	if f.CurrentContext == "" {
		return Selected{}, nil
	}
	var ctx *Context
	for i := range f.Contexts {
		if f.Contexts[i].Name == f.CurrentContext {
			ctx = &f.Contexts[i].Context
		}
	}
	if ctx == nil {
		return Selected{}, errorf("config file %s: current-context %q names no context", f.path, f.CurrentContext)
	}
	var cluster *Cluster
	for i := range f.Clusters {
		if f.Clusters[i].Name == ctx.Cluster {
			cluster = &f.Clusters[i].Cluster
		}
	}
	if cluster == nil {
		return Selected{}, errorf("config file %s: context %q names no cluster: %q is not defined", f.path, f.CurrentContext, ctx.Cluster)
	}
	if cluster.Server == "" {
		return Selected{}, errorf("config file %s: cluster %q has no server", f.path, ctx.Cluster)
	}
	sel := Selected{Server: cluster.Server, CA: f.resolve(cluster.CertificateAuthority)}
	if ctx.User == "" {
		return sel, nil
	}
	for _, u := range f.Users {
		if u.Name != ctx.User {
			continue
		}
		if (u.User.ClientCertificate == "") != (u.User.ClientKey == "") {
			return Selected{}, errorf("config file %s: user %q needs client-certificate and client-key together", f.path, u.Name)
		}
		sel.Cert, sel.Key = f.resolve(u.User.ClientCertificate), f.resolve(u.User.ClientKey)
		return sel, nil
	}
	return Selected{}, errorf("config file %s: context %q names user %q, which is not defined", f.path, f.CurrentContext, ctx.User)
}

// resolve makes a relative path relative to the directory of the file.
func (f *File) resolve(p string) string {
	if p == "" || filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(filepath.Dir(f.path), p)
}

// UseContext sets the current context to name and writes the file back. The
// file's comments and layout are not kept.
func (f *File) UseContext(name string) error {
	for _, c := range f.Contexts {
		if c.Name != name {
			continue
		}
		f.CurrentContext = name
		data, err := yaml.Marshal(f)
		if err != nil {
			return fmt.Errorf("config file %s: %w", f.path, err)
		}
		if err := os.WriteFile(f.path, data, 0o600); err != nil {
			return fmt.Errorf("config file %s: %w", f.path, err)
		}
		return nil
	}
	return errorf("config file %s: no context %q", f.path, name)
}
