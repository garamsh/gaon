package scheduler

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/garamsh/gaon/lib/api"
)

// The apiVersion and kind of a scheduler configuration file.
const (
	ConfigAPIVersion = "scheduler.gaon.garam.sh/v1alpha1"
	ConfigKind       = "SchedulerConfiguration"
)

const (
	defaultWeight          = 1
	defaultExtenderTimeout = 5 * time.Second
)

// Config is a validated scheduler configuration. The zero Config enables no
// score plugin and no extender.
type Config struct {
	scores    []weightedScore
	extenders []*extender
}

type weightedScore struct {
	plugin scorePlugin
	weight int
}

type configFile struct {
	APIVersion   string            `yaml:"apiVersion"`
	Kind         string            `yaml:"kind"`
	ScorePlugins []scorePluginFile `yaml:"scorePlugins"`
	Extenders    []extenderFile    `yaml:"extenders"`
}

type scorePluginFile struct {
	Name   string `yaml:"name"`
	Weight *int   `yaml:"weight"`
}

type extenderFile struct {
	URLPrefix      string         `yaml:"urlPrefix"`
	FilterVerb     string         `yaml:"filterVerb"`
	PrioritizeVerb string         `yaml:"prioritizeVerb"`
	Weight         *int           `yaml:"weight"`
	Timeout        *time.Duration `yaml:"timeout"`
	Ignorable      bool           `yaml:"ignorable"`
	TLSCAFile      string         `yaml:"tlsCAFile"`
	TLSCertFile    string         `yaml:"tlsCertFile"`
	TLSKeyFile     string         `yaml:"tlsKeyFile"`
}

// LoadConfig reads and validates the configuration file at path, and builds
// the HTTP client of each extender from the TLS files it names. The file is
// read once; the Scheduler does not watch it.
func LoadConfig(path string) (Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return Config{}, fmt.Errorf("config: %w", err)
	}
	defer func() { _ = f.Close() }()
	dec := yaml.NewDecoder(f)
	dec.KnownFields(true)
	var file configFile
	if err := dec.Decode(&file); err != nil {
		return Config{}, fmt.Errorf("config %s: %w", path, err)
	}
	if err := dec.Decode(new(configFile)); !errors.Is(err, io.EOF) {
		return Config{}, fmt.Errorf("config %s: holds more than one document", path)
	}
	cfg, err := file.build()
	if err != nil {
		return Config{}, fmt.Errorf("config %s: %w", path, err)
	}
	return cfg, nil
}

func (f configFile) build() (Config, error) {
	if f.APIVersion != ConfigAPIVersion || f.Kind != ConfigKind {
		return Config{}, fmt.Errorf("apiVersion %q and kind %q, want %q and %q", f.APIVersion, f.Kind, ConfigAPIVersion, ConfigKind)
	}
	var cfg Config
	seen := map[string]bool{}
	for _, p := range f.ScorePlugins {
		plugin, ok := scorePlugins[p.Name]
		switch {
		case !ok:
			return Config{}, fmt.Errorf("scorePlugins: unknown plugin %q", p.Name)
		case seen[p.Name]:
			return Config{}, fmt.Errorf("scorePlugins: plugin %q is named twice", p.Name)
		}
		seen[p.Name] = true
		w, err := weightOf(p.Weight)
		if err != nil {
			return Config{}, fmt.Errorf("scorePlugins: %s: %w", p.Name, err)
		}
		cfg.scores = append(cfg.scores, weightedScore{plugin: plugin, weight: w})
	}
	for i, e := range f.Extenders {
		ext, err := e.build()
		if err != nil {
			return Config{}, fmt.Errorf("extenders[%d]: %w", i, err)
		}
		cfg.extenders = append(cfg.extenders, ext)
	}
	return cfg, nil
}

func weightOf(w *int) (int, error) {
	if w == nil {
		return defaultWeight, nil
	}
	if *w < 1 {
		return 0, fmt.Errorf("weight %d is below 1", *w)
	}
	return *w, nil
}

func (e extenderFile) build() (*extender, error) {
	u, err := url.Parse(e.URLPrefix)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("urlPrefix %q is not a URL with a host", e.URLPrefix)
	}
	if e.FilterVerb == "" && e.PrioritizeVerb == "" {
		return nil, errors.New("neither filterVerb nor prioritizeVerb is set")
	}
	weight, err := weightOf(e.Weight)
	if err != nil {
		return nil, err
	}
	timeout := defaultExtenderTimeout
	if e.Timeout != nil {
		timeout = *e.Timeout
	}
	if timeout <= 0 {
		return nil, fmt.Errorf("timeout %s is not positive", timeout)
	}
	client, err := e.client(u)
	if err != nil {
		return nil, err
	}
	client.Timeout = timeout
	// A redirect would send the MicroVM to a host the file never named.
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &extender{
		urlPrefix:      e.URLPrefix,
		filterVerb:     e.FilterVerb,
		prioritizeVerb: e.PrioritizeVerb,
		weight:         weight,
		ignorable:      e.Ignorable,
		client:         client,
	}, nil
}

// client builds the HTTP client for u: https needs a CA file and takes a client
// certificate when one is given; plain http is for a loopback address only.
func (e extenderFile) client(u *url.URL) (*http.Client, error) {
	switch u.Scheme {
	case "https":
		switch {
		case e.TLSCAFile == "":
			return nil, errors.New("an https urlPrefix needs tlsCAFile")
		case (e.TLSCertFile == "") != (e.TLSKeyFile == ""):
			return nil, errors.New("give both tlsCertFile and tlsKeyFile or neither")
		case e.TLSCertFile == "":
			return api.NewServerTLSClient(e.TLSCAFile)
		}
		return api.NewTLSClient(e.TLSCAFile, e.TLSCertFile, e.TLSKeyFile)
	case "http":
		if ip := net.ParseIP(u.Hostname()); ip == nil || !ip.IsLoopback() {
			return nil, fmt.Errorf("an http urlPrefix must name a loopback address, not %q", u.Hostname())
		}
		if e.TLSCAFile != "" || e.TLSCertFile != "" || e.TLSKeyFile != "" {
			return nil, errors.New("tls files are for an https urlPrefix")
		}
		return &http.Client{}, nil
	default:
		return nil, fmt.Errorf("urlPrefix scheme %q is not http or https", u.Scheme)
	}
}
