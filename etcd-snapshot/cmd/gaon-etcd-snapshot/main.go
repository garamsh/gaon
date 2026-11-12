// Command gaon-etcd-snapshot takes one snapshot of the etcd cluster, checks it
// and uploads it to S3 with credentials from IAM Roles Anywhere. It runs once
// and exits: 0 when the snapshot is stored, non-zero otherwise.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/garamsh/gaon/etcd-snapshot/internal/rolesanywhere"
	"github.com/garamsh/gaon/etcd-snapshot/internal/snapshot"
	"github.com/garamsh/gaon/etcd-snapshot/internal/snapshot/etcd"
	"github.com/garamsh/gaon/etcd-snapshot/internal/snapshot/s3"
	"github.com/garamsh/gaon/etcd-snapshot/internal/snapshot/systemclock"
)

// config is what the unit is started with.
type config struct {
	etcd          etcd.Config
	rolesAnywhere rolesanywhere.Config
	s3            s3.Config
	prefix        string
	dir           string
	timeout       time.Duration
}

// parseFlags reads the flags in args. It returns flag.ErrHelp when usage was
// asked for, after writing it to out.
func parseFlags(args []string, out io.Writer) (config, error) {
	fs := flag.NewFlagSet("gaon-etcd-snapshot", flag.ContinueOnError)
	fs.SetOutput(out)
	var cfg config
	endpoints := fs.String("etcd-endpoints", "http://127.0.0.1:2379", "comma separated etcd client endpoints")
	fs.StringVar(&cfg.etcd.CAFile, "etcd-ca-file", "", "PEM file of the CA that signs etcd's certificate; given with etcd-cert-file and etcd-key-file")
	fs.StringVar(&cfg.etcd.CertFile, "etcd-cert-file", "", "PEM file of the client certificate presented to etcd; given with etcd-ca-file and etcd-key-file")
	fs.StringVar(&cfg.etcd.KeyFile, "etcd-key-file", "", "PEM file of the client certificate's key; given with etcd-ca-file and etcd-cert-file")
	fs.DurationVar(&cfg.etcd.DialTimeout, "etcd-dial-timeout", 5*time.Second, "time allowed to reach etcd")
	fs.StringVar(&cfg.s3.Bucket, "s3-bucket", "", "bucket the snapshot is uploaded to")
	fs.StringVar(&cfg.s3.Region, "region", "", "AWS region of the bucket and of IAM Roles Anywhere")
	fs.StringVar(&cfg.s3.Endpoint, "s3-endpoint", "", "replaces the S3 endpoint, such as http://127.0.0.1:9000, and selects path-style addressing")
	fs.StringVar(&cfg.prefix, "s3-prefix", "", "start of every object key, such as etcd/")
	fs.StringVar(&cfg.rolesAnywhere.CertFile, "rolesanywhere-cert-file", "", "PEM file of the certificate, CN "+rolesanywhere.CommonName+", signed by gaon's CA")
	fs.StringVar(&cfg.rolesAnywhere.KeyFile, "rolesanywhere-key-file", "", "PEM file of the certificate's key")
	fs.StringVar(&cfg.rolesAnywhere.TrustAnchorARN, "rolesanywhere-trust-anchor-arn", "", "ARN of the Roles Anywhere trust anchor holding gaon's CA")
	fs.StringVar(&cfg.rolesAnywhere.ProfileARN, "rolesanywhere-profile-arn", "", "ARN of the Roles Anywhere profile")
	fs.StringVar(&cfg.rolesAnywhere.RoleARN, "rolesanywhere-role-arn", "", "ARN of the role the session assumes")
	fs.StringVar(&cfg.rolesAnywhere.Endpoint, "rolesanywhere-endpoint", "", "replaces the Roles Anywhere endpoint")
	fs.StringVar(&cfg.dir, "snapshot-dir", "", "directory the snapshot is written to before the upload; empty is the system's temporary directory")
	fs.DurationVar(&cfg.timeout, "timeout", 15*time.Minute, "time allowed for the whole run")
	if err := fs.Parse(args); err != nil {
		return config{}, err
	}
	for _, e := range strings.Split(*endpoints, ",") {
		if e = strings.TrimSpace(e); e != "" {
			cfg.etcd.Endpoints = append(cfg.etcd.Endpoints, e)
		}
	}
	if len(cfg.etcd.Endpoints) == 0 {
		return config{}, errors.New("no etcd endpoint given")
	}
	if cfg.etcd.DialTimeout <= 0 || cfg.timeout <= 0 {
		return config{}, fmt.Errorf("etcd dial timeout %s and timeout %s must be positive", cfg.etcd.DialTimeout, cfg.timeout)
	}
	if err := cfg.etcd.Validate(); err != nil {
		return config{}, err
	}
	if cfg.s3.Bucket == "" {
		return config{}, errors.New("no s3-bucket given")
	}
	cfg.rolesAnywhere.Region = cfg.s3.Region
	if err := cfg.rolesAnywhere.Validate(); err != nil {
		return config{}, err
	}

	return cfg, nil
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := execute(ctx, os.Args[1:], os.Stderr, systemclock.Clock{})
	stop()
	os.Exit(code)
}

// execute parses args, runs once and returns the process's exit code: 0 for a
// stored snapshot or a usage request, 2 for flags that are refused and 1 for a
// run that failed. Its log goes to out.
func execute(ctx context.Context, args []string, out io.Writer, clock snapshot.Clock) int {
	log := slog.New(slog.NewTextHandler(out, nil))
	cfg, err := parseFlags(args, out)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		log.Error("start", "error", err)
		return 2
	}
	if err := run(ctx, cfg, clock, log); err != nil {
		log.Error("snapshot failed", "error", err)
		return 1
	}
	return 0
}

// run takes one snapshot and stores it, within cfg.timeout.
func run(ctx context.Context, cfg config, clock snapshot.Clock, log *slog.Logger) error {
	ctx, cancel := context.WithTimeout(ctx, cfg.timeout)
	defer cancel()

	source, err := etcd.New(cfg.etcd)
	if err != nil {
		return err
	}
	defer func() { _ = source.Close() }()

	cfg.s3.Credentials, err = rolesanywhere.New(cfg.rolesAnywhere)
	if err != nil {
		return err
	}
	store, err := s3.New(cfg.s3)
	if err != nil {
		return err
	}
	return snapshot.NewService(source, store, clock, snapshot.Config{Dir: cfg.dir, Prefix: cfg.prefix}, log).Run(ctx)
}
