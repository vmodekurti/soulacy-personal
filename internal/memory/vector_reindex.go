package memory

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var vectorDimensionPattern = regexp.MustCompile(`(?i)embedding\s+float\[(\d+)\]`)

var ErrVectorReindexConflict = errors.New("vector reindex state belongs to a different migration")

type VectorReindexOptions struct {
	TargetDims int
	ModelID    string
	BatchSize  int
	Restart    bool
	Progress   func(VectorReindexProgress)
}

type VectorReindexProgress struct {
	SourceDims int   `json:"source_dims"`
	TargetDims int   `json:"target_dims"`
	Completed  int64 `json:"completed"`
	Total      int64 `json:"total"`
}

type VectorReindexReport struct {
	SourceDims int           `json:"source_dims"`
	TargetDims int           `json:"target_dims"`
	Rows       int64         `json:"rows"`
	Resumed    bool          `json:"resumed"`
	Noop       bool          `json:"noop"`
	Duration   time.Duration `json:"duration"`
}

// VectorBatchEmbedder lets remote providers amortize request overhead during
// a rebuild. ReindexVectorStore falls back to Embed when it is unavailable.
type VectorBatchEmbedder interface {
	EmbedBatch(context.Context, []string) ([][]float32, error)
}

// VectorDimensions reads sqlite-vec's declared dimension from sqlite_schema.
// A missing index returns zero without error, allowing a rebuild from metadata.
func VectorDimensions(ctx context.Context, db *sql.DB) (int, error) {
	var ddl string
	err := db.QueryRowContext(ctx, `SELECT sql FROM sqlite_schema WHERE type='table' AND name='memory_vectors'`).Scan(&ddl)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	match := vectorDimensionPattern.FindStringSubmatch(ddl)
	if len(match) != 2 {
		return 0, fmt.Errorf("vector memory: cannot determine dimensions from schema %q", ddl)
	}
	return strconv.Atoi(match[1])
}

// ReindexVectorStore rebuilds memory_vectors from memory_vector_meta. Embedding
// work is checkpointed in a regular staging table, so cancellation or provider
// failure can resume. The final drop/create/load is one SQLite transaction;
// the old index remains intact if validation or cutover fails.
func ReindexVectorStore(ctx context.Context, db *sql.DB, embedder Embedder, opts VectorReindexOptions) (VectorReindexReport, error) {
	started := time.Now()
	if db == nil || embedder == nil {
		return VectorReindexReport{}, errors.New("vector reindex requires a database and embedder")
	}
	if opts.TargetDims <= 0 {
		return VectorReindexReport{}, errors.New("vector reindex target dimensions must be positive")
	}
	if opts.BatchSize <= 0 {
		opts.BatchSize = 64
	}
	if opts.BatchSize > 1000 {
		opts.BatchSize = 1000
	}
	opts.ModelID = strings.TrimSpace(opts.ModelID)
	if opts.ModelID == "" {
		return VectorReindexReport{}, errors.New("vector reindex model ID is required")
	}

	sourceDims, err := VectorDimensions(ctx, db)
	if err != nil {
		return VectorReindexReport{}, err
	}
	report := VectorReindexReport{SourceDims: sourceDims, TargetDims: opts.TargetDims}
	if sourceDims == opts.TargetDims {
		report.Noop, report.Duration = true, time.Since(started)
		return report, nil
	}
	if err := ensureVectorReindexSchema(ctx, db); err != nil {
		return report, err
	}

	var stateSource, stateTarget int
	var stateModel string
	var lastRowID, targetMax int64
	stateErr := db.QueryRowContext(ctx, `SELECT source_dims,target_dims,model_id,last_rowid,target_max_rowid FROM memory_vector_reindex_state WHERE singleton=1`).
		Scan(&stateSource, &stateTarget, &stateModel, &lastRowID, &targetMax)
	if stateErr == nil {
		if stateSource != sourceDims || stateTarget != opts.TargetDims || stateModel != opts.ModelID {
			if !opts.Restart {
				return report, fmt.Errorf("%w: have %d→%d model %q; requested %d→%d model %q (use --restart)",
					ErrVectorReindexConflict, stateSource, stateTarget, stateModel, sourceDims, opts.TargetDims, opts.ModelID)
			}
			if err := resetVectorReindex(ctx, db); err != nil {
				return report, err
			}
			stateErr = sql.ErrNoRows
		} else {
			report.Resumed = lastRowID > 0
		}
	} else if !errors.Is(stateErr, sql.ErrNoRows) {
		return report, stateErr
	}
	if errors.Is(stateErr, sql.ErrNoRows) {
		if err := db.QueryRowContext(ctx, `SELECT COALESCE(MAX(rowid),0) FROM memory_vector_meta`).Scan(&targetMax); err != nil {
			return report, err
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO memory_vector_reindex_state(singleton,source_dims,target_dims,model_id,last_rowid,target_max_rowid,started_at)
			VALUES(1,?,?,?,?,?,?)`, sourceDims, opts.TargetDims, opts.ModelID, 0, targetMax, time.Now().UTC()); err != nil {
			return report, err
		}
	}

	for {
		rows, err := db.QueryContext(ctx, `SELECT rowid,workspace_id,content FROM memory_vector_meta WHERE rowid>? AND rowid<=? ORDER BY rowid LIMIT ?`, lastRowID, targetMax, opts.BatchSize)
		if err != nil {
			return report, err
		}
		type source struct {
			rowID       int64
			workspaceID string
			content     string
		}
		sources := make([]source, 0, opts.BatchSize)
		for rows.Next() {
			var item source
			if err := rows.Scan(&item.rowID, &item.workspaceID, &item.content); err != nil {
				rows.Close()
				return report, err
			}
			sources = append(sources, item)
		}
		if err := rows.Close(); err != nil {
			return report, err
		}

		type staged struct {
			rowID       int64
			workspaceID string
			embedding   string
		}
		batch := make([]staged, 0, len(sources))
		vectors := make([][]float32, 0, len(sources))
		if len(sources) > 0 {
			if batcher, ok := embedder.(VectorBatchEmbedder); ok {
				texts := make([]string, len(sources))
				for i := range sources {
					texts[i] = sources[i].content
				}
				vectors, err = batcher.EmbedBatch(ctx, texts)
				if err != nil {
					return report, fmt.Errorf("vector reindex batch after row %d: %w", lastRowID, err)
				}
				if len(vectors) != len(sources) {
					return report, fmt.Errorf("vector reindex provider returned %d vectors for %d rows", len(vectors), len(sources))
				}
			} else {
				for _, item := range sources {
					vec, embedErr := embedder.Embed(ctx, item.content)
					if embedErr != nil {
						return report, fmt.Errorf("vector reindex row %d: %w", item.rowID, embedErr)
					}
					vectors = append(vectors, vec)
				}
			}
		}
		for i, vec := range vectors {
			rowID := sources[i].rowID
			if len(vec) != opts.TargetDims {
				return report, fmt.Errorf("vector reindex row %d: embedder returned %d dims, expected %d", rowID, len(vec), opts.TargetDims)
			}
			raw, err := json.Marshal(vec)
			if err != nil {
				return report, err
			}
			batch = append(batch, staged{rowID: rowID, workspaceID: sources[i].workspaceID, embedding: string(raw)})
		}
		if len(batch) > 0 {
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				return report, err
			}
			for _, item := range batch {
				if _, err = tx.ExecContext(ctx, `INSERT OR REPLACE INTO memory_vector_reindex_stage(rowid,workspace_id,embedding) VALUES(?,?,?)`, item.rowID, item.workspaceID, item.embedding); err != nil {
					_ = tx.Rollback()
					return report, err
				}
				lastRowID = item.rowID
			}
			if _, err = tx.ExecContext(ctx, `UPDATE memory_vector_reindex_state SET last_rowid=? WHERE singleton=1`, lastRowID); err != nil {
				_ = tx.Rollback()
				return report, err
			}
			if err = tx.Commit(); err != nil {
				return report, err
			}
		}
		var completed int64
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM memory_vector_reindex_stage`).Scan(&completed); err != nil {
			return report, err
		}
		if opts.Progress != nil {
			opts.Progress(VectorReindexProgress{SourceDims: sourceDims, TargetDims: opts.TargetDims, Completed: completed, Total: targetMax})
		}
		if len(batch) > 0 {
			continue
		}
		var currentMax int64
		if err := db.QueryRowContext(ctx, `SELECT COALESCE(MAX(rowid),0) FROM memory_vector_meta`).Scan(&currentMax); err != nil {
			return report, err
		}
		if currentMax > targetMax {
			targetMax = currentMax
			if _, err := db.ExecContext(ctx, `UPDATE memory_vector_reindex_state SET target_max_rowid=? WHERE singleton=1`, targetMax); err != nil {
				return report, err
			}
			continue
		}
		break
	}

	rows, err := cutoverVectorIndex(ctx, db, opts.TargetDims)
	if err != nil {
		return report, err
	}
	report.Rows, report.Duration = rows, time.Since(started)
	return report, nil
}

func ensureVectorReindexSchema(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS memory_vector_reindex_state(
		singleton INTEGER PRIMARY KEY CHECK(singleton=1), source_dims INTEGER NOT NULL, target_dims INTEGER NOT NULL,
		model_id TEXT NOT NULL, last_rowid INTEGER NOT NULL, target_max_rowid INTEGER NOT NULL, started_at DATETIME NOT NULL);
		CREATE TABLE IF NOT EXISTS memory_vector_reindex_stage(
			rowid INTEGER PRIMARY KEY, workspace_id TEXT NOT NULL, embedding TEXT NOT NULL);
		CREATE INDEX IF NOT EXISTS idx_memory_vector_reindex_stage_workspace
			ON memory_vector_reindex_stage(workspace_id)`)
	return err
}

func resetVectorReindex(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, `DELETE FROM memory_vector_reindex_stage`); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM memory_vector_reindex_state`); err != nil {
		return err
	}
	return tx.Commit()
}

func cutoverVectorIndex(ctx context.Context, db *sql.DB, dims int) (int64, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, `DELETE FROM memory_vector_reindex_stage
		WHERE NOT EXISTS (
			SELECT 1 FROM memory_vector_meta
			WHERE memory_vector_meta.rowid = memory_vector_reindex_stage.rowid
			  AND memory_vector_meta.workspace_id = memory_vector_reindex_stage.workspace_id
		)`); err != nil {
		return 0, err
	}
	var metaCount, stageCount int64
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM memory_vector_meta`).Scan(&metaCount); err != nil {
		return 0, err
	}
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM memory_vector_reindex_stage`).Scan(&stageCount); err != nil {
		return 0, err
	}
	if metaCount != stageCount {
		return 0, fmt.Errorf("vector reindex source changed during rebuild: metadata=%d staged=%d; retry with the gateway stopped", metaCount, stageCount)
	}
	if _, err = tx.ExecContext(ctx, `DROP TABLE IF EXISTS memory_vectors`); err != nil {
		return 0, err
	}
	if _, err = tx.ExecContext(ctx, fmt.Sprintf(`CREATE VIRTUAL TABLE memory_vectors USING vec0(embedding float[%d])`, dims)); err != nil {
		return 0, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO memory_vectors(rowid,embedding) SELECT rowid,embedding FROM memory_vector_reindex_stage ORDER BY rowid`); err != nil {
		return 0, err
	}
	var vectorCount int64
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM memory_vectors`).Scan(&vectorCount); err != nil {
		return 0, err
	}
	if vectorCount != metaCount {
		return 0, fmt.Errorf("vector reindex verification failed: vectors=%d metadata=%d", vectorCount, metaCount)
	}
	if _, err = tx.ExecContext(ctx, `DROP TABLE memory_vector_reindex_stage`); err != nil {
		return 0, err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM memory_vector_reindex_state`); err != nil {
		return 0, err
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	return vectorCount, nil
}
