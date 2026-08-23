// Package artifactstore provides the shared binary-object boundary used by
// Scale gateways and execution workers. Metadata remains in Soulacy's scoped
// databases; bytes live behind opaque workspace-prefixed keys.
package artifactstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

var ErrNotFound = errors.New("artifactstore: object not found")

type Object struct {
	Body        io.ReadCloser
	Size        int64
	ContentType string
}

type Store interface {
	Put(ctx context.Context, key string, body io.Reader, size int64, contentType string) error
	Open(ctx context.Context, key string) (Object, error)
	DeletePrefix(ctx context.Context, prefix string) (int64, error)
	Close() error
}

// WorkspacePrefix is the only supported top-level object layout. Callers add
// run/session-specific suffixes below it; no caller may choose another tenant.
func WorkspacePrefix(workspaceID string) string {
	return "workspaces/" + strings.TrimSpace(workspaceID) + "/"
}

func validKey(key string) error {
	if key == "" || strings.HasPrefix(key, "/") || strings.ContainsRune(key, '\\') {
		return fmt.Errorf("artifactstore: invalid object key %q", key)
	}
	clean := path.Clean(key)
	if clean == "." || clean != key || clean == ".." || strings.HasPrefix(clean, "../") {
		return fmt.Errorf("artifactstore: invalid object key %q", key)
	}
	return nil
}

// OpenStore resolves s3://bucket/prefix and file:///shared/mount roots. The
// file backend is for a genuinely shared POSIX volume, not node-local disk.
func OpenStore(ctx context.Context, raw string) (Store, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, fmt.Errorf("artifactstore: parse root: %w", err)
	}
	switch strings.ToLower(u.Scheme) {
	case "s3":
		if u.Host == "" {
			return nil, errors.New("artifactstore: s3 bucket is required")
		}
		cfg, err := awsconfig.LoadDefaultConfig(ctx)
		if err != nil {
			return nil, fmt.Errorf("artifactstore: load AWS configuration: %w", err)
		}
		return &s3Store{client: s3.NewFromConfig(cfg), bucket: u.Host, prefix: strings.Trim(strings.TrimSpace(u.Path), "/")}, nil
	case "file":
		if u.Host != "" && u.Host != "localhost" {
			return nil, errors.New("artifactstore: file root must not name a remote host")
		}
		root, err := filepath.Abs(filepath.FromSlash(u.Path))
		if err != nil || strings.TrimSpace(u.Path) == "" {
			return nil, errors.New("artifactstore: absolute file root is required")
		}
		if err = os.MkdirAll(root, 0o700); err != nil {
			return nil, fmt.Errorf("artifactstore: create shared root: %w", err)
		}
		return &fileStore{root: root}, nil
	default:
		return nil, fmt.Errorf("artifactstore: unsupported root scheme %q (use s3 or file)", u.Scheme)
	}
}

type fileStore struct{ root string }

func (s *fileStore) objectPath(key string) (string, error) {
	if err := validKey(key); err != nil {
		return "", err
	}
	target := filepath.Join(s.root, filepath.FromSlash(key))
	rel, err := filepath.Rel(s.root, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("artifactstore: key escapes shared root")
	}
	return target, nil
}

func (s *fileStore) Put(_ context.Context, key string, body io.Reader, _ int64, _ string) error {
	target, err := s.objectPath(key)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), ".artifact-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) //nolint:errcheck
	if _, err = io.Copy(tmp, body); err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Chmod(tmpName, 0o600); err != nil {
		return err
	}
	return os.Rename(tmpName, target)
}

func (s *fileStore) Open(_ context.Context, key string) (Object, error) {
	target, err := s.objectPath(key)
	if err != nil {
		return Object{}, err
	}
	f, err := os.Open(target)
	if os.IsNotExist(err) {
		return Object{}, ErrNotFound
	}
	if err != nil {
		return Object{}, err
	}
	info, err := f.Stat()
	if err != nil || info.IsDir() {
		_ = f.Close()
		if err == nil {
			err = ErrNotFound
		}
		return Object{}, err
	}
	return Object{Body: f, Size: info.Size(), ContentType: mimeType(target)}, nil
}

func (s *fileStore) DeletePrefix(_ context.Context, prefix string) (int64, error) {
	prefix = strings.TrimSuffix(prefix, "/")
	target, err := s.objectPath(prefix)
	if err != nil {
		return 0, err
	}
	var count int64
	_ = filepath.WalkDir(target, func(_ string, entry os.DirEntry, walkErr error) error {
		if walkErr == nil && !entry.IsDir() {
			count++
		}
		return nil
	})
	if err = os.RemoveAll(target); err != nil {
		return 0, err
	}
	return count, nil
}
func (*fileStore) Close() error { return nil }

type s3API interface {
	PutObject(context.Context, *s3.PutObjectInput, ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	GetObject(context.Context, *s3.GetObjectInput, ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	ListObjectsV2(context.Context, *s3.ListObjectsV2Input, ...func(*s3.Options)) (*s3.ListObjectsV2Output, error)
	DeleteObjects(context.Context, *s3.DeleteObjectsInput, ...func(*s3.Options)) (*s3.DeleteObjectsOutput, error)
}
type s3Store struct {
	client         s3API
	bucket, prefix string
}

func (s *s3Store) key(key string) (string, error) {
	if err := validKey(key); err != nil {
		return "", err
	}
	if s.prefix == "" {
		return key, nil
	}
	return s.prefix + "/" + key, nil
}
func (s *s3Store) Put(ctx context.Context, key string, body io.Reader, size int64, contentType string) error {
	key, err := s.key(key)
	if err != nil {
		return err
	}
	_, err = s.client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key), Body: body, ContentLength: aws.Int64(size), ContentType: aws.String(contentType), ServerSideEncryption: types.ServerSideEncryptionAwsKms})
	return err
}
func (s *s3Store) Open(ctx context.Context, key string) (Object, error) {
	key, err := s.key(key)
	if err != nil {
		return Object{}, err
	}
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil {
		var apiErr smithy.APIError
		if errors.As(err, &apiErr) && (apiErr.ErrorCode() == "NoSuchKey" || apiErr.ErrorCode() == "NotFound") {
			return Object{}, ErrNotFound
		}
		return Object{}, fmt.Errorf("artifactstore: get s3 object: %w", err)
	}
	return Object{Body: out.Body, Size: aws.ToInt64(out.ContentLength), ContentType: aws.ToString(out.ContentType)}, nil
}
func (s *s3Store) DeletePrefix(ctx context.Context, prefix string) (int64, error) {
	prefix, err := s.key(strings.TrimSuffix(prefix, "/") + "/sentinel")
	if err != nil {
		return 0, err
	}
	prefix = strings.TrimSuffix(prefix, "sentinel")
	var count int64
	var token *string
	for {
		page, err := s.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: aws.String(s.bucket), Prefix: aws.String(prefix), ContinuationToken: token})
		if err != nil {
			return count, err
		}
		objects := make([]types.ObjectIdentifier, 0, len(page.Contents))
		for _, object := range page.Contents {
			objects = append(objects, types.ObjectIdentifier{Key: object.Key})
		}
		if len(objects) > 0 {
			if _, err = s.client.DeleteObjects(ctx, &s3.DeleteObjectsInput{Bucket: aws.String(s.bucket), Delete: &types.Delete{Objects: objects, Quiet: aws.Bool(true)}}); err != nil {
				return count, err
			}
			count += int64(len(objects))
		}
		if !aws.ToBool(page.IsTruncated) {
			return count, nil
		}
		token = page.NextContinuationToken
	}
}
func (*s3Store) Close() error { return nil }

func mimeType(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".json":
		return "application/json"
	case ".csv":
		return "text/csv"
	case ".html":
		return "text/html; charset=utf-8"
	case ".md", ".txt", ".log":
		return "text/plain; charset=utf-8"
	case ".pdf":
		return "application/pdf"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	default:
		return "application/octet-stream"
	}
}
