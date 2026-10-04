package backup

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// ObjectStore is the minimal blob storage the backups need. Keys use "/"
// separators.
type ObjectStore interface {
	Put(ctx context.Context, key string, body []byte) error
	Get(ctx context.Context, key string) (io.ReadCloser, error)
	// List returns all keys with the prefix, sorted.
	List(ctx context.Context, prefix string) ([]string, error)
	// Delete removes an object permanently. On a versioned bucket that means
	// every stored version, not just hiding it behind a delete marker.
	Delete(ctx context.Context, key string) error
	String() string
}

// DirStore stores objects as files under a local directory. Used in tests
// and for trying backups/restores locally without Spaces.
type DirStore struct{ Root string }

func (d DirStore) path(key string) string { return filepath.Join(d.Root, filepath.FromSlash(key)) }

func (d DirStore) Put(_ context.Context, key string, body []byte) error {
	p := d.path(key)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return writeFileAtomic(p, body)
}

func (d DirStore) Get(_ context.Context, key string) (io.ReadCloser, error) {
	return os.Open(d.path(key))
}

func (d DirStore) List(_ context.Context, prefix string) ([]string, error) {
	var keys []string
	err := filepath.WalkDir(d.Root, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if e.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(d.Root, p)
		if err != nil {
			return err
		}
		if key := filepath.ToSlash(rel); strings.HasPrefix(key, prefix) {
			keys = append(keys, key)
		}
		return nil
	})
	sort.Strings(keys)
	return keys, err
}

func (d DirStore) Delete(_ context.Context, key string) error {
	err := os.Remove(d.path(key))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

func (d DirStore) String() string { return "dir:" + d.Root }

// S3Config configures an S3-compatible store such as DigitalOcean Spaces.
type S3Config struct {
	Endpoint  string // e.g. https://nyc3.digitaloceanspaces.com
	Region    string // e.g. nyc3
	Bucket    string
	KeyID     string
	SecretKey string
}

type S3Store struct {
	client *s3.Client
	bucket string
}

func NewS3Store(c S3Config) *S3Store {
	client := s3.New(s3.Options{
		Region:       c.Region,
		BaseEndpoint: aws.String(c.Endpoint),
		Credentials:  credentials.NewStaticCredentialsProvider(c.KeyID, c.SecretKey, ""),
		// S3-compatible stores don't all support the SDK's default
		// flexible checksums; only send them when the API requires it.
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
	})
	return &S3Store{client: client, bucket: c.Bucket}
}

func (s *S3Store) Put(ctx context.Context, key string, body []byte) error {
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
		Body:   bytes.NewReader(body),
		ACL:    "private",
	})
	if err != nil {
		return fmt.Errorf("put %s: %w", key, err)
	}
	return nil
}

func (s *S3Store) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil {
		return nil, fmt.Errorf("get %s: %w", key, err)
	}
	return out.Body, nil
}

func (s *S3Store) List(ctx context.Context, prefix string) ([]string, error) {
	var keys []string
	p := s3.NewListObjectsV2Paginator(s.client, &s3.ListObjectsV2Input{
		Bucket: aws.String(s.bucket),
		Prefix: aws.String(prefix),
	})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("list %s: %w", prefix, err)
		}
		for _, o := range page.Contents {
			keys = append(keys, aws.ToString(o.Key))
		}
	}
	sort.Strings(keys)
	return keys, nil
}

func (s *S3Store) Delete(ctx context.Context, key string) error {
	var versions []*string
	p := s3.NewListObjectVersionsPaginator(s.client, &s3.ListObjectVersionsInput{
		Bucket: aws.String(s.bucket),
		Prefix: aws.String(key),
	})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return fmt.Errorf("list versions of %s: %w", key, err)
		}
		for _, v := range page.Versions {
			if aws.ToString(v.Key) == key {
				versions = append(versions, v.VersionId)
			}
		}
		for _, m := range page.DeleteMarkers {
			if aws.ToString(m.Key) == key {
				versions = append(versions, m.VersionId)
			}
		}
	}
	if len(versions) == 0 {
		versions = []*string{nil} // unversioned bucket: a plain delete
	}
	for _, v := range versions {
		_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
			Bucket:    aws.String(s.bucket),
			Key:       aws.String(key),
			VersionId: v,
		})
		if err != nil {
			return fmt.Errorf("delete %s: %w", key, err)
		}
	}
	return nil
}

func (s *S3Store) String() string { return "s3://" + s.bucket }

func writeFileAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// Prefixed keeps every object under a prefix ("prod/db/…", "demo/photos/…")
// so several deployments can share one bucket without seeing — or pruning —
// each other's objects. An empty prefix means the bucket's top level.
func Prefixed(store ObjectStore, prefix string) ObjectStore {
	prefix = strings.Trim(prefix, "/")
	if prefix == "" {
		return store
	}
	return prefixStore{store, prefix + "/"}
}

type prefixStore struct {
	inner  ObjectStore
	prefix string
}

func (p prefixStore) Put(ctx context.Context, key string, body []byte) error {
	return p.inner.Put(ctx, p.prefix+key, body)
}

func (p prefixStore) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	return p.inner.Get(ctx, p.prefix+key)
}

func (p prefixStore) Delete(ctx context.Context, key string) error {
	return p.inner.Delete(ctx, p.prefix+key)
}

func (p prefixStore) List(ctx context.Context, prefix string) ([]string, error) {
	keys, err := p.inner.List(ctx, p.prefix+prefix)
	for i, k := range keys {
		keys[i] = strings.TrimPrefix(k, p.prefix)
	}
	return keys, err
}

func (p prefixStore) String() string {
	return p.inner.String() + "/" + strings.TrimSuffix(p.prefix, "/")
}
