package workspaceexport

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/soulacy/soulacy/internal/ownership"
	"github.com/soulacy/soulacy/internal/wsroot"
)

// ErrNotFound is returned for an export ID this workspace does not have.
//
// Deliberately the same error for "no such export" and "that export belongs to
// another workspace". Distinguishing them would turn the download route into
// an oracle that confirms an ID exists somewhere, which is the whole value of
// an opaque ID.
var ErrNotFound = errors.New("workspaceexport: export not found")

// ErrNotReady is returned when an archive is asked for before it exists.
var ErrNotReady = errors.New("workspaceexport: export is not ready")

// ErrExpired is returned when an archive has aged out.
var ErrExpired = errors.New("workspaceexport: export has expired")

// JobStatus is where one export request has got to.
//
// The Job prefix is not decoration. EntryStatus and JobStatus are both short
// lowercase words in the same package and both end up in the same JSON
// document; an unprefixed `Failed` in each is two identifiers one letter of
// context apart, and the compiler cannot tell you which one you meant when
// both are untyped constants assigned to a string field.
type JobStatus string

const (
	JobPending JobStatus = "pending"
	JobRunning JobStatus = "running"
	JobReady   JobStatus = "ready"
	JobFailed  JobStatus = "failed"
	JobExpired JobStatus = "expired"
)

// DefaultTTL is how long a finished archive stays downloadable.
//
// An export is a full copy of a workspace's data sitting on disk in a form
// that needs no credential beyond the download URL, so it is the highest-value
// object this system creates. Short enough that a forgotten export is not a
// permanent second copy of the workspace; long enough that a customer who
// asked on Friday can still fetch it on Monday.
const DefaultTTL = 72 * time.Hour

// Job is one export request.
type Job struct {
	ID            string    `json:"id"`
	WorkspaceID   string    `json:"workspace_id"`
	WorkspaceName string    `json:"workspace_name,omitempty"`
	RequestedBy   string    `json:"requested_by"`
	Status        JobStatus `json:"status"`
	CreatedAt     time.Time `json:"created_at"`
	StartedAt     time.Time `json:"started_at,omitempty"`
	CompletedAt   time.Time `json:"completed_at,omitempty"`
	ExpiresAt     time.Time `json:"expires_at,omitempty"`
	Bytes         int64     `json:"bytes,omitempty"`
	// Error is a reason, never a value read out of the workspace.
	Error string `json:"error,omitempty"`
	// Manifest is present once the export has finished, so a caller can see
	// what the archive contains — and what it does not — without downloading
	// it.
	Manifest *Manifest `json:"manifest,omitempty"`
}

// Expired reports whether the archive has aged out as of now. Computed rather
// than stored, so a process that was not running when the deadline passed
// still answers correctly the moment it starts.
func (j *Job) Expired(now time.Time) bool {
	return !j.ExpiresAt.IsZero() && now.After(j.ExpiresAt)
}

// Source produces one resource class's bytes for one workspace.
//
// Emit returns false when the workspace has none of this resource, which the
// manifest records as "empty" rather than "exported" with a zero-byte file.
// The distinction is the one a customer cares about: an empty file looks like
// a broken exporter and an "empty" status looks like an empty workspace.
type Source struct {
	Resource string
	// Filename is the name inside the archive's resources/ directory. Chosen
	// by the Source because only it knows whether it is producing JSON, CSV or
	// a nested archive.
	Filename string
	Emit     func(ctx context.Context, w io.Writer) (bool, error)
}

// validExportID is the path guard.
//
// Export IDs become directory names under the workspace root, so this is the
// boundary between an opaque identifier and a filesystem traversal. Narrow by
// construction: no dots at all, which removes "." and ".." without having to
// enumerate them and removes every encoding of them a decoder might produce.
var validExportID = regexp.MustCompile(`^[a-zA-Z0-9_-]{8,64}$`)

// Store holds one deployment's exports, namespaced by workspace.
//
// Namespaced by PATH, not by a workspace_id column on a shared index. The
// difference matters at the one moment it is tested: a handler that forgets to
// filter reads another tenant's rows, whereas a handler that forgets to pass a
// workspace here reads the personal root and finds nothing.
type Store struct {
	root   string
	layout wsroot.Layout
}

// NewStore roots a store at base. base is the deployment data directory; each
// workspace's exports live beneath its own wsroot directory.
func NewStore(base string) *Store { return &Store{root: base} }

// SetWorkspaceLayoutRoot enables the coherent Team/Scale layout. Personal
// mode deliberately leaves this unset and retains its historical paths.
func (s *Store) SetWorkspaceLayoutRoot(root string) { s.layout = wsroot.NewLayout(root) }

// dir resolves one export's directory, refusing an ID that could escape.
func (s *Store) dir(workspaceID, exportID string) (string, error) {
	if !validExportID.MatchString(exportID) {
		return "", ErrNotFound
	}
	return filepath.Join(s.workspaceDir(workspaceID), exportID), nil
}

func (s *Store) workspaceDir(workspaceID string) string {
	return filepath.Join(s.layout.Dir(s.root, wsroot.Normalize(workspaceID)), "exports")
}

const (
	jobFile      = "job.json"
	manifestFile = "manifest.json"
	archiveFile  = "archive.tar.gz"
)

// Save writes a job record. Written whole to a temporary file and renamed, so
// a reader never sees a half-written job — a status poller hitting a torn file
// would report "failed" for an export that is running fine.
func (s *Store) Save(job *Job) error {
	dir, err := s.dir(job.WorkspaceID, job.ID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(job, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(dir, jobFile), encoded, 0o600)
}

// Get returns one job, reporting expiry as a status rather than hiding it.
func (s *Store) Get(workspaceID, exportID string, now time.Time) (*Job, error) {
	dir, err := s.dir(workspaceID, exportID)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(filepath.Join(dir, jobFile))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	var job Job
	if err := json.Unmarshal(raw, &job); err != nil {
		return nil, err
	}
	if job.Status == JobReady && job.Expired(now) {
		job.Status = JobExpired
	}
	return &job, nil
}

// List returns a workspace's exports, newest first.
func (s *Store) List(workspaceID string, now time.Time) ([]*Job, error) {
	entries, err := os.ReadDir(s.workspaceDir(workspaceID))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var jobs []*Job
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		job, err := s.Get(workspaceID, entry.Name(), now)
		if err != nil {
			// A directory that is not a readable export is skipped rather
			// than failing the list. One corrupt record must not make a
			// customer's other exports unreachable.
			continue
		}
		jobs = append(jobs, job)
	}
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].CreatedAt.After(jobs[j].CreatedAt) })
	return jobs, nil
}

// Open returns the archive for a finished export.
//
// The caller closes the file. It is NOT closed by a defer here and must not be
// closed by a defer in an HTTP handler either: Fiber streams a response body
// after the handler returns, so a deferred Close makes every download fail
// with "file already closed" — and it fails on the byte range, not on the
// call, so the handler still logs a 200.
func (s *Store) Open(workspaceID, exportID string, now time.Time) (*os.File, *Job, error) {
	job, err := s.Get(workspaceID, exportID, now)
	if err != nil {
		return nil, nil, err
	}
	switch job.Status {
	case JobReady:
	case JobExpired:
		return nil, job, ErrExpired
	case JobFailed:
		return nil, job, ErrNotReady
	default:
		return nil, job, ErrNotReady
	}
	dir, err := s.dir(workspaceID, exportID)
	if err != nil {
		return nil, job, err
	}
	file, err := os.Open(filepath.Join(dir, archiveFile))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, job, ErrNotFound
		}
		return nil, job, err
	}
	return file, job, nil
}

// Purge removes an export's bytes. Used by expiry and by workspace deletion.
func (s *Store) Purge(workspaceID, exportID string) error {
	dir, err := s.dir(workspaceID, exportID)
	if err != nil {
		return err
	}
	return os.RemoveAll(dir)
}

// PurgeExpired deletes the archives of exports past their TTL, leaving the job
// record so a caller asking about one is told it expired rather than that it
// never existed. The record is small; the archive is the workspace.
func (s *Store) PurgeExpired(workspaceID string, now time.Time) (int, error) {
	jobs, err := s.List(workspaceID, now)
	if err != nil {
		return 0, err
	}
	purged := 0
	for _, job := range jobs {
		if job.Status != JobExpired {
			continue
		}
		dir, err := s.dir(workspaceID, job.ID)
		if err != nil {
			continue
		}
		if err := os.Remove(filepath.Join(dir, archiveFile)); err != nil && !os.IsNotExist(err) {
			continue
		}
		job.Status = JobExpired
		job.Bytes = 0
		if err := s.Save(job); err == nil {
			purged++
		}
	}
	return purged, nil
}

// Options configures one export run.
type Options struct {
	Job     *Job
	Sources []Source
	Now     func() time.Time
	// Ctx bounds the RUN, and is deliberately separate from the request that
	// asked for it.
	//
	// The gateway detaches this work with context.WithoutCancel so a client
	// that hangs up mid-upload does not abandon a half-written archive. But
	// "detached from the request" must not become "uncancellable": a shutting
	// down process still has to be able to stop, and an export of a large
	// workspace can run for minutes. So the run takes its own context and
	// checks it between resource classes.
	Ctx context.Context
	TTL time.Duration
}

// Run produces the archive for one export job.
//
// It writes each resource to a temporary file first, hashes it, and assembles
// the tar afterwards. The streaming alternative — tar entries written as they
// are produced — would put manifest.json LAST, because its checksums are not
// known until every entry is written, and a manifest last is a manifest a
// consumer cannot read without buffering the whole archive. The temporary
// files cost disk we are already spending.
func Run(store *Store, opts Options) (err error) {
	job := opts.Job
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	ctx := opts.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	ttl := opts.TTL
	if ttl <= 0 {
		ttl = DefaultTTL
	}

	job.Status = JobRunning
	job.StartedAt = now().UTC()
	if err := store.Save(job); err != nil {
		return err
	}
	// Any exit from here on records an outcome. A job left "running" forever
	// is the worst of the three outcomes to present to a customer: it is
	// indistinguishable from slow, so nobody retries and nobody reports it.
	defer func() {
		job.CompletedAt = now().UTC()
		if err != nil {
			job.Status = JobFailed
			job.Error = err.Error()
		}
		if saveErr := store.Save(job); saveErr != nil && err == nil {
			err = saveErr
		}
	}()

	dir, dirErr := store.dir(job.WorkspaceID, job.ID)
	if dirErr != nil {
		return dirErr
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	staging := filepath.Join(dir, "staging")
	if err := os.MkdirAll(staging, 0o700); err != nil {
		return err
	}
	defer os.RemoveAll(staging)

	plan := ownership.WorkspaceExportPlan()
	manifest := newManifest(job.ID, job.WorkspaceID, job.WorkspaceName, job.RequestedBy, plan, now())

	byResource := map[string]Source{}
	for _, source := range opts.Sources {
		byResource[source.Resource] = source
	}

	var files []stagedFile

	for _, entry := range plan {
		if err := ctx.Err(); err != nil {
			return err
		}
		source, ok := byResource[entry.Resource]
		if !ok {
			continue // stays "not-exported", with the seeded explanation
		}
		filename := strings.TrimSpace(source.Filename)
		if filename == "" {
			filename = entry.Resource + ".json"
		}
		path := filepath.Join(staging, sanitizeFilename(filename))
		wrote, sum, size, emitErr := emitTo(ctx, path, source)
		switch {
		case emitErr != nil:
			// One resource failing does not abandon the export. A customer
			// with an archive containing 26 of 27 classes and a manifest
			// naming the 27th is strictly better served than one with no
			// archive at all — and the manifest makes the gap explicit, so
			// nobody mistakes the partial for a complete copy.
			manifest.set(entry.Resource, func(e *Entry) {
				e.Status = StatusFailed
				e.Detail = emitErr.Error()
			})
		case !wrote:
			manifest.set(entry.Resource, func(e *Entry) {
				e.Status = StatusEmpty
				e.Detail = ""
			})
		default:
			manifest.set(entry.Resource, func(e *Entry) {
				e.Status = StatusExported
				e.Path = "resources/" + filepath.Base(path)
				e.Bytes = size
				e.SHA256 = sum
				e.Detail = ""
			})
			files = append(files, stagedFile{resource: entry.Resource, filename: filepath.Base(path), path: path})
		}
	}
	manifest.seal()
	job.Manifest = manifest

	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	// The manifest is written beside the archive as well as inside it, so
	// status and list can answer what an export contains without unpacking a
	// multi-gigabyte file — and so it survives PurgeExpired removing the
	// archive.
	if err := writeFileAtomic(filepath.Join(dir, manifestFile), encoded, 0o600); err != nil {
		return err
	}

	size, err := writeArchive(ctx, filepath.Join(dir, archiveFile), encoded, files)
	if err != nil {
		return err
	}

	job.Bytes = size
	job.Status = JobReady
	job.ExpiresAt = now().UTC().Add(ttl)
	return nil
}

// emitTo runs one Source into a file, hashing as it writes.
func emitTo(ctx context.Context, path string, source Source) (wrote bool, sum string, size int64, err error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return false, "", 0, err
	}
	defer file.Close()
	digest := sha256.New()
	counter := &countingWriter{}
	wrote, err = source.Emit(ctx, io.MultiWriter(file, digest, counter))
	if err != nil {
		return false, "", 0, err
	}
	if !wrote {
		return false, "", 0, nil
	}
	if err := file.Sync(); err != nil {
		return false, "", 0, err
	}
	return true, hex.EncodeToString(digest.Sum(nil)), counter.n, nil
}

type countingWriter struct{ n int64 }

func (w *countingWriter) Write(p []byte) (int, error) {
	w.n += int64(len(p))
	return len(p), nil
}

// stagedFile is one resource's bytes waiting to be folded into the archive.
// A named type rather than an anonymous struct because it crosses a function
// boundary: []stagedFile and []struct{...} have identical underlying types and
// are still not assignable to one another.
type stagedFile struct{ resource, filename, path string }

// writeArchive assembles the tar.gz with manifest.json first.
func writeArchive(ctx context.Context, path string, manifest []byte, files []stagedFile) (int64, error) {
	// Written to .partial and renamed. A download route that found a
	// half-written archive would serve a truncated copy of a workspace and
	// report success, which is the one failure a customer cannot detect
	// without the checksum they are downloading the archive to obtain.
	partial := path + ".partial"
	out, err := os.OpenFile(partial, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, err
	}
	cleanup := func() { out.Close(); os.Remove(partial) }

	gz := gzip.NewWriter(out)
	tw := tar.NewWriter(gz)

	if err := writeTarFile(tw, manifestFile, manifest); err != nil {
		cleanup()
		return 0, err
	}
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			cleanup()
			return 0, err
		}
		body, err := os.Open(file.path)
		if err != nil {
			cleanup()
			return 0, err
		}
		info, err := body.Stat()
		if err != nil {
			body.Close()
			cleanup()
			return 0, err
		}
		header := &tar.Header{
			Name:    "resources/" + file.filename,
			Mode:    0o600,
			Size:    info.Size(),
			ModTime: info.ModTime().UTC(),
		}
		if err := tw.WriteHeader(header); err != nil {
			body.Close()
			cleanup()
			return 0, err
		}
		if _, err := io.Copy(tw, body); err != nil {
			body.Close()
			cleanup()
			return 0, err
		}
		body.Close()
	}
	if err := tw.Close(); err != nil {
		cleanup()
		return 0, err
	}
	if err := gz.Close(); err != nil {
		cleanup()
		return 0, err
	}
	if err := out.Sync(); err != nil {
		cleanup()
		return 0, err
	}
	size, err := out.Seek(0, io.SeekCurrent)
	if err != nil {
		cleanup()
		return 0, err
	}
	if err := out.Close(); err != nil {
		os.Remove(partial)
		return 0, err
	}
	if err := os.Rename(partial, path); err != nil {
		os.Remove(partial)
		return 0, err
	}
	return size, nil
}

func writeTarFile(tw *tar.Writer, name string, body []byte) error {
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(body))}); err != nil {
		return err
	}
	_, err := tw.Write(body)
	return err
}

// sanitizeFilename keeps a Source's chosen name inside the staging directory.
// A Source is in-process code rather than user input, but it frequently names
// its file after something a user supplied, and the difference between those
// two is not visible at this call site.
func sanitizeFilename(name string) string {
	name = filepath.Base(filepath.Clean("/" + name))
	if name == "." || name == string(filepath.Separator) || name == "" {
		return "resource.json"
	}
	return name
}

func writeFileAtomic(path string, body []byte, mode os.FileMode) error {
	partial := path + ".partial"
	if err := os.WriteFile(partial, body, mode); err != nil {
		return err
	}
	if err := os.Rename(partial, path); err != nil {
		os.Remove(partial)
		return err
	}
	return nil
}

// NewJob mints an export request.
func NewJob(id, workspaceID, workspaceName, requestedBy string, at time.Time) (*Job, error) {
	if !validExportID.MatchString(id) {
		return nil, fmt.Errorf("workspaceexport: %q is not a usable export ID", id)
	}
	return &Job{
		ID:            id,
		WorkspaceID:   workspaceID,
		WorkspaceName: workspaceName,
		RequestedBy:   requestedBy,
		Status:        JobPending,
		CreatedAt:     at.UTC(),
	}, nil
}
