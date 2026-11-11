package apiserver

import (
	"errors"
	"flag"
	"fmt"
	"strings"
	"time"
)

// Config is how gaon-apiserver is run.
type Config struct {
	// Listen is the local address the API is served on.
	Listen string
	// Endpoints are the etcd client endpoints of the cell's Store.
	Endpoints []string
	// DialTimeout bounds the connection to etcd at start.
	DialTimeout time.Duration
}

// ParseConfig reads the flags in args.
func ParseConfig(args []string) (Config, error) {
	fs := flag.NewFlagSet("gaon-apiserver", flag.ContinueOnError)
	cfg := Config{}
	endpoints := fs.String("etcd-endpoints", "http://127.0.0.1:2379", "comma separated etcd client endpoints")
	fs.StringVar(&cfg.Listen, "listen", "127.0.0.1:8080", "local address to serve the API on")
	fs.DurationVar(&cfg.DialTimeout, "etcd-dial-timeout", 5*time.Second, "time allowed to reach etcd at start")
	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}
	for _, e := range strings.Split(*endpoints, ",") {
		if e = strings.TrimSpace(e); e != "" {
			cfg.Endpoints = append(cfg.Endpoints, e)
		}
	}
	if len(cfg.Endpoints) == 0 {
		return Config{}, errors.New("no etcd endpoint given")
	}
	if cfg.DialTimeout <= 0 {
		return Config{}, fmt.Errorf("etcd dial timeout %s is not positive", cfg.DialTimeout)
	}
	return cfg, nil
}
