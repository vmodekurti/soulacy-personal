// worker.go — the background ingestion worker.
//
// It claims queued jobs from the durable catalog, streams the spooled bytes
// through the existing extract → chunk → embed → store pipeline, reports
// progress, and retries transient failures with bounded exponential backoff.
// Permanently-failed jobs are parked as `failed` (and handed to the dead-letter
// sink when one is wired) rather than vanishing.
//
// Why a claim-loop rather than a queue subscription: internal/queue is an
// at-most-once in-memory bus that DROPS messages on overflow and whose Ack() is
// a no-op. A dropped ingest = a document silently missing from the KB. The
// SQLite catalog is therefore the source of truth; the worker polls it (and can
// be nudged awake for latency), and a startup sweep requeues anything a crash
// left mid-flight.

package knowledge

import (
	"context"
	"fmt"
	"os"
	"runtime/debug"
	"time"

	"go.uber.org/zap"
)

// ProgressSink receives job lifecycle updates so the GUI can show live progress.
// Implemented by the gateway (EventHub); nil is fine.
type ProgressSink interface {
	IngestProgress(job IngestJob)
}

// DeadLetterSink receives jobs that exhausted their attempt budget.
// Implemented by the gateway's DLQ store; nil is fine.
type DeadLetterSink interface {
	IngestDeadLetter(job IngestJob, err error)
}

// WorkerOptions tunes the worker. Zero values get sane defaults.
type WorkerOptions struct {
	// PollInterval is how often the worker looks for queued work when idle.
	PollInterval time.Duration
	// MaxAttempts bounds retries of a single job.
	MaxAttempts int
	// BaseBackoff is the first retry delay; it doubles per attempt.
	BaseBackoff time.Duration
	// MaxBackoff caps the retry delay.
	MaxBackoff time.Duration
	// MaxDocumentBytes rejects oversized spooled jobs before reading them into
	// memory. 0 means the production default.
	MaxDocumentBytes int64
}

func (o WorkerOptions) withDefaults() WorkerOptions {
	if o.PollInterval <= 0 {
		o.PollInterval = 2 * time.Second
	}
	if o.MaxAttempts <= 0 {
		o.MaxAttempts = DefaultMaxAttempts
	}
	if o.BaseBackoff <= 0 {
		o.BaseBackoff = 2 * time.Second
	}
	if o.MaxBackoff <= 0 {
		o.MaxBackoff = 60 * time.Second
	}
	if o.MaxDocumentBytes <= 0 {
		o.MaxDocumentBytes = DefaultMaxDocumentBytes
	}
	return o
}

// Worker drains the ingestion job catalog.
type Worker struct {
	svc  *Service
	opts WorkerOptions
	log  *zap.Logger

	progress ProgressSink
	dlq      DeadLetterSink

	wake chan struct{} // non-blocking nudge so a fresh upload starts immediately
}

// NewWorker builds an ingestion worker over a knowledge Service.
func NewWorker(svc *Service, opts WorkerOptions, log *zap.Logger) *Worker {
	if log == nil {
		log = zap.NewNop()
	}
	return &Worker{
		svc:  svc,
		opts: opts.withDefaults(),
		log:  log,
		wake: make(chan struct{}, 1),
	}
}

// SetProgressSink wires live progress reporting.
func (w *Worker) SetProgressSink(p ProgressSink) { w.progress = p }

// SetDeadLetterSink wires the permanent-failure sink.
func (w *Worker) SetDeadLetterSink(d DeadLetterSink) { w.dlq = d }

// Nudge wakes the worker immediately (called right after enqueueing) so an
// upload doesn't wait out the poll interval. Never blocks.
func (w *Worker) Nudge() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// Start recovers anything a crash left mid-flight, then runs the claim loop
// until ctx is cancelled.
func (w *Worker) Start(ctx context.Context) {
	if w.svc == nil || w.svc.Store == nil {
		return
	}
	if n, err := w.svc.Store.RecoverStaleIngests(); err != nil {
		w.log.Warn("knowledge: could not recover in-flight ingest jobs", zap.Error(err))
	} else if n > 0 {
		w.log.Info("knowledge: requeued ingest jobs interrupted by a restart", zap.Int("jobs", n))
	}

	go w.loop(ctx)
}

func (w *Worker) loop(ctx context.Context) {
	t := time.NewTicker(w.opts.PollInterval)
	defer t.Stop()
	for {
		// Drain everything currently queued before idling again.
		for {
			if ctx.Err() != nil {
				return
			}
			worked, err := w.safeStep(ctx)
			if err != nil {
				w.log.Warn("knowledge: ingest worker step failed", zap.Error(err))
			}
			if !worked {
				break
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-w.wake:
		case <-t.C:
		}
	}
}

// safeStep is step with a panic barrier.
//
// step parses attacker-supplied bytes: a .pdf goes to a third-party PDF parser,
// a .docx to encoding/xml, and either can be malformed on purpose. This worker
// runs on its own goroutine, outside fiber's recover middleware, so a panic in
// an extractor took the WHOLE GATEWAY down — every agent, every channel — from
// one bad upload. The HTTP path that reaches the same extractors survives such a
// panic; this one did not, which is an inconsistency rather than a decision.
//
// A panic is reported as an ordinary job error so the job follows the normal
// retry-then-park path and the operator sees the reason on the job row.
func (w *Worker) safeStep(ctx context.Context) (worked bool, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("knowledge: ingest worker panicked: %v", r)
			w.log.Error("knowledge: ingest worker panicked", zap.Any("panic", r), zap.ByteString("stack", debug.Stack()))
			worked = true // something was claimed; keep the loop moving
		}
	}()
	return w.step(ctx)
}

// step claims and runs at most one job. Reports whether it did work.
func (w *Worker) step(ctx context.Context) (bool, error) {
	job, ok, err := w.svc.Store.ClaimNextIngest()
	if err != nil || !ok {
		return false, err
	}
	w.emit(job)

	doc, ingestErr := w.run(ctx, job)
	if ingestErr == nil {
		done, ferr := w.svc.Store.FinishIngest(job.ID, JobDone, docID(doc), "")
		if ferr != nil {
			return true, ferr
		}
		_ = os.Remove(job.SpoolPath) // the bytes are in the KB now
		w.emit(done)
		w.log.Info("knowledge: ingested document",
			zap.String("kb", job.KBName), zap.String("title", job.Title), zap.String("doc", docID(doc)))
		return true, nil
	}

	// A cancelled context is a shutdown, not a failure — leave the job for the
	// next process to pick up rather than burning an attempt.
	if ctx.Err() != nil {
		if _, err := w.svc.Store.RequeueIngest(job.ID, false); err != nil {
			w.log.Warn("knowledge: could not requeue on shutdown", zap.Error(err))
		}
		return false, nil
	}

	// Transient failure with budget left → back off and retry.
	if job.Attempt < w.opts.MaxAttempts {
		delay := w.backoff(job.Attempt)
		w.log.Warn("knowledge: ingest failed, will retry",
			zap.String("job", job.ID), zap.Int("attempt", job.Attempt),
			zap.Duration("retry_in", delay), zap.Error(ingestErr))
		go func(id string, d time.Duration) {
			select {
			case <-ctx.Done():
				return
			case <-time.After(d):
			}
			if _, err := w.svc.Store.RequeueIngest(id, false); err == nil {
				w.Nudge()
			}
		}(job.ID, delay)
		return true, nil
	}

	// Budget spent → park it. The row (and the reason) survive for the operator.
	failed, ferr := w.svc.Store.FinishIngest(job.ID, JobFailed, "", ingestErr.Error())
	if ferr != nil {
		return true, ferr
	}
	// The spool file was only deleted on the SUCCESS path, so every permanently
	// failed job kept up to knowledge.max_document_bytes (50 MB by default) of
	// disk forever. Nothing will read it again — the job is parked and its reason
	// is on the row — so it goes now.
	_ = os.Remove(job.SpoolPath)
	if w.dlq != nil {
		w.dlq.IngestDeadLetter(failed, ingestErr)
	}
	w.emit(failed)
	w.log.Error("knowledge: ingest permanently failed",
		zap.String("job", job.ID), zap.Int("attempts", job.Attempt), zap.Error(ingestErr))
	return true, nil
}

// run streams the spooled bytes through the ingestion pipeline.
func (w *Worker) run(ctx context.Context, job IngestJob) (*Document, error) {
	if w.opts.MaxDocumentBytes > 0 && job.ByteSize > w.opts.MaxDocumentBytes {
		return nil, fmt.Errorf("document is %s, over the configured knowledge.max_document_bytes limit of %s", formatBytes(job.ByteSize), formatBytes(w.opts.MaxDocumentBytes))
	}
	if st, err := os.Stat(job.SpoolPath); err == nil && w.opts.MaxDocumentBytes > 0 && st.Size() > w.opts.MaxDocumentBytes {
		return nil, fmt.Errorf("spooled document is %s, over the configured knowledge.max_document_bytes limit of %s", formatBytes(st.Size()), formatBytes(w.opts.MaxDocumentBytes))
	}
	data, err := os.ReadFile(job.SpoolPath)
	if err != nil {
		return nil, fmt.Errorf("read spooled upload: %w", err)
	}
	text, err := ExtractText(job.MIMEType, data)
	if err != nil {
		return nil, fmt.Errorf("extract text: %w", err)
	}
	data = nil // release the raw bytes before embedding

	// Progress is reported per embedding batch by the service.
	report := func(pct int) {
		if err := w.svc.Store.SetIngestProgress(job.ID, pct); err != nil {
			return
		}
		if w.progress != nil {
			j := job
			j.Status = JobRunning
			j.Progress = pct
			w.progress.IngestProgress(j)
		}
	}
	return w.svc.ingestExtracted(ctx, job.KBName, job.Title, job.Source, job.MIMEType, text, report)
}

func (w *Worker) backoff(attempt int) time.Duration {
	d := w.opts.BaseBackoff
	for i := 1; i < attempt; i++ {
		d *= 2
		if d >= w.opts.MaxBackoff {
			return w.opts.MaxBackoff
		}
	}
	if d > w.opts.MaxBackoff {
		d = w.opts.MaxBackoff
	}
	return d
}

func (w *Worker) emit(j IngestJob) {
	if w.progress != nil {
		w.progress.IngestProgress(j)
	}
}

func docID(d *Document) string {
	if d == nil {
		return ""
	}
	return d.ID
}

func formatBytes(n int64) string {
	const unit = int64(1024)
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := unit, 0
	for v := n / unit; v >= unit && exp < 4; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
