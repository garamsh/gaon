// Package s3 is the snapshot Store backed by an S3 bucket.
package s3

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/garamsh/gaon/etcd-snapshot/internal/snapshot"
)

// MaxObjectSize is the largest object one PutObject takes, 5 GiB. A larger
// snapshot is refused, since a multipart upload is not implemented.
const MaxObjectSize = 5 << 30

// Config says where the snapshots go.
type Config struct {
	// Bucket is the bucket's name.
	Bucket string
	// Region is the bucket's region.
	Region string
	// Credentials provide the AWS credentials of every request.
	Credentials aws.CredentialsProvider
	// Endpoint, where not empty, replaces the S3 endpoint and selects path-style
	// addressing.
	Endpoint string
}

// Store puts snapshots in a bucket.
type Store struct {
	client *awss3.Client
	bucket string
}

// New returns a Store.
func New(cfg Config) (*Store, error) {
	if cfg.Bucket == "" {
		return nil, errors.New("no bucket given")
	}
	if cfg.Region == "" {
		return nil, errors.New("no region given")
	}
	if cfg.Credentials == nil {
		return nil, errors.New("no credentials given")
	}
	opts := awss3.Options{
		Region:      cfg.Region,
		Credentials: cfg.Credentials,
	}
	if cfg.Endpoint != "" {
		opts.BaseEndpoint = aws.String(cfg.Endpoint)
		opts.UsePathStyle = true
	}
	return &Store{client: awss3.New(opts), bucket: cfg.Bucket}, nil
}

// Put uploads o with its SHA-256 as the checksum S3 verifies, so that S3
// refuses and stores nothing for bytes that differ from o.SHA256.
func (s *Store) Put(ctx context.Context, o snapshot.Object) error {
	if o.Size > MaxObjectSize {
		return fmt.Errorf("snapshot of %d bytes exceeds the %d bytes of one upload", o.Size, int64(MaxObjectSize))
	}
	_, err := s.client.PutObject(ctx, &awss3.PutObjectInput{
		Bucket:            aws.String(s.bucket),
		Key:               aws.String(o.Key),
		Body:              o.Body,
		ContentLength:     aws.Int64(o.Size),
		ChecksumAlgorithm: types.ChecksumAlgorithmSha256,
		ChecksumSHA256:    aws.String(base64.StdEncoding.EncodeToString(o.SHA256[:])),
	})
	if err != nil {
		return fmt.Errorf("put s3://%s/%s: %w", s.bucket, o.Key, err)
	}
	return nil
}
