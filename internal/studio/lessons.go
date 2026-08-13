package studio

import (
	"context"
	"crypto/sha1"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	sqlite_vec "github.com/asg017/sqlite-vec-go-bindings/cgo"
	"github.com/soulacy/soulacy/internal/sqlitex"
)

// Normalized L2 is converted to confidence as 1-distance/2. Orthogonal vectors
// score ~0.293, so 0.32 excludes unrelated lessons while retaining nearby
// concepts and exact-tool lessons with otherwise different wording.
const LessonSimilarityThreshold = 0.32

type Lesson struct {
	ID           string    `json:"id"`
	Tool         string    `json:"tool,omitempty"`
	Class        string    `json:"class,omitempty"`
	ObservedKeys []string  `json:"observed_keys,omitempty"`
	Guidance     string    `json:"guidance"`
	Count        int       `json:"count"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

func LessonFromProposal(p RepairProposal, tool, intent string) (Lesson, bool) {
	if p.Class != RepairShapeDrift && p.Class != RepairTemplateError {
		return Lesson{}, false
	}
	guidance := strings.TrimSpace(p.Rationale)
	if guidance == "" {
		return Lesson{}, false
	}
	if tool != "" {
		guidance = "When using `" + tool + "`: " + guidance
	}
	now := time.Now().UTC()
	l := Lesson{Tool: tool, Class: string(p.Class), ObservedKeys: p.ObservedKeys, Guidance: guidance, Count: 1, CreatedAt: now, UpdatedAt: now}
	l.ID = lessonID(l)
	return l, true
}

func lessonID(l Lesson) string {
	h := sha1.Sum([]byte(strings.ToLower(l.Tool + "\x00" + l.Guidance)))
	return hex.EncodeToString(h[:8])
}

// LessonEmbedder is the narrow seam used by semantic lesson memory. Production
// wires the configured default embedding provider/model; tests can use a fake.
type LessonEmbedder interface {
	Embed(context.Context, string) ([]float32, error)
}

type lessonEmbedderIdentity interface{ Identity() string }

type localLessonEmbedder struct{}

func (localLessonEmbedder) Identity() string { return "local-hash-v1:128" }

func (localLessonEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
	const dims = 128
	vector := make([]float32, dims)
	for term := range semanticTerms(text) {
		h := fnv.New32a()
		_, _ = h.Write([]byte(term))
		n := h.Sum32()
		idx := int(n % dims)
		sign := float32(1)
		if n&(1<<31) != 0 {
			sign = -1
		}
		vector[idx] += sign
	}
	normalizeVector(vector)
	return vector, nil
}

// LessonStore is an embedded SQLite + sqlite-vec semantic store. An existing
// JSON array at path is automatically preserved as .legacy.json and imported.
type LessonStore struct {
	path     string
	embedder LessonEmbedder
	mu       sync.Mutex
	initOnce sync.Once
	initErr  error
	db       *sql.DB
}

func (s *LessonStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

var lessonVecAuto sync.Once

func NewLessonStore(path string) *LessonStore {
	return NewSemanticLessonStore(path, localLessonEmbedder{})
}

func NewSemanticLessonStore(path string, embedder LessonEmbedder) *LessonStore {
	if embedder == nil {
		embedder = localLessonEmbedder{}
	}
	return &LessonStore{path: path, embedder: embedder}
}

func (s *LessonStore) init() error {
	s.initOnce.Do(func() { s.initErr = s.initialize() })
	return s.initErr
}

func (s *LessonStore) initialize() error {
	if strings.TrimSpace(s.path) == "" {
		return errors.New("lesson store path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	var legacy []Lesson
	if raw, err := os.ReadFile(s.path); err == nil && len(strings.TrimSpace(string(raw))) > 0 && strings.HasPrefix(strings.TrimSpace(string(raw)), "[") {
		if err := json.Unmarshal(raw, &legacy); err != nil {
			return fmt.Errorf("parse legacy lessons: %w", err)
		}
		backup := s.path + ".legacy.json"
		if err := os.Rename(s.path, backup); err != nil {
			return fmt.Errorf("preserve legacy lessons: %w", err)
		}
	}
	lessonVecAuto.Do(sqlite_vec.Auto)
	db, err := sqlitex.Open(s.path, sqlitex.DefaultOptions())
	if err != nil {
		return err
	}
	s.db = db
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS lessons (
		id TEXT PRIMARY KEY, tool TEXT, class TEXT, observed_keys TEXT,
		guidance TEXT NOT NULL, count INTEGER NOT NULL,
		created_at DATETIME NOT NULL, updated_at DATETIME NOT NULL
	)`); err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS lesson_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)`); err != nil {
		return err
	}
	for _, lesson := range legacy {
		if err := s.addDB(context.Background(), lesson); err != nil {
			return fmt.Errorf("import legacy lesson %s: %w", lesson.ID, err)
		}
	}
	return nil
}

func (s *LessonStore) ensureVectorTable(ctx context.Context, dim int) error {
	identity := "unknown"
	if identified, ok := s.embedder.(lessonEmbedderIdentity); ok {
		identity = identified.Identity()
	}
	var stored int
	err := s.db.QueryRow(`SELECT value FROM lesson_meta WHERE key='dimensions'`).Scan(&stored)
	if err == sql.ErrNoRows {
		if _, err := s.db.Exec(fmt.Sprintf(`CREATE VIRTUAL TABLE IF NOT EXISTS lesson_vectors USING vec0(id TEXT PRIMARY KEY, embedding FLOAT[%d])`, dim)); err != nil {
			return err
		}
		if _, err = s.db.Exec(`INSERT INTO lesson_meta(key,value) VALUES('dimensions',?)`, dim); err != nil {
			return err
		}
		_, err = s.db.Exec(`INSERT INTO lesson_meta(key,value) VALUES('embedding_identity',?)`, identity)
		return err
	}
	if err != nil {
		return err
	}
	if stored != dim {
		return s.rebuildVectorIndex(ctx, dim, identity)
	}
	var storedIdentity string
	if err := s.db.QueryRow(`SELECT value FROM lesson_meta WHERE key='embedding_identity'`).Scan(&storedIdentity); err == sql.ErrNoRows {
		_, _ = s.db.Exec(`INSERT INTO lesson_meta(key,value) VALUES('embedding_identity',?)`, identity)
	} else if err != nil {
		return err
	} else if storedIdentity != identity {
		return s.rebuildVectorIndex(ctx, dim, identity)
	}
	return nil
}

func (s *LessonStore) rebuildVectorIndex(ctx context.Context, dim int, identity string) error {
	if _, err := s.db.Exec(`DROP TABLE IF EXISTS lesson_vectors`); err != nil {
		return err
	}
	if _, err := s.db.Exec(fmt.Sprintf(`CREATE VIRTUAL TABLE lesson_vectors USING vec0(id TEXT PRIMARY KEY, embedding FLOAT[%d])`, dim)); err != nil {
		return err
	}
	if _, err := s.db.Exec(`INSERT INTO lesson_meta(key,value) VALUES('dimensions',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, dim); err != nil {
		return err
	}
	if _, err := s.db.Exec(`INSERT INTO lesson_meta(key,value) VALUES('embedding_identity',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, identity); err != nil {
		return err
	}
	lessons := s.All()
	for _, lesson := range lessons {
		vector, err := s.embedder.Embed(ctx, lessonEmbeddingText(lesson))
		if err != nil {
			return err
		}
		normalizeVector(vector)
		blob, err := sqlite_vec.SerializeFloat32(vector)
		if err != nil {
			return err
		}
		if _, err := s.db.ExecContext(ctx, `INSERT INTO lesson_vectors(id,embedding) VALUES(?,?)`, lesson.ID, blob); err != nil {
			return err
		}
	}
	return nil
}

func (s *LessonStore) Add(l Lesson) error {
	return s.AddContext(context.Background(), l)
}

func (s *LessonStore) AddContext(ctx context.Context, l Lesson) error {
	guidance, safe := sanitizeLearnedText(l.Guidance, 800)
	if !safe {
		return nil
	}
	l.Guidance = guidance
	if l.ID == "" {
		l.ID = lessonID(l)
	}
	if err := s.init(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.addDB(ctx, l)
}

func (s *LessonStore) addDB(ctx context.Context, l Lesson) error {
	text := lessonEmbeddingText(l)
	vector, err := s.embedder.Embed(ctx, text)
	if err != nil {
		return fmt.Errorf("embed lesson: %w", err)
	}
	if len(vector) == 0 {
		return errors.New("embed lesson: empty vector")
	}
	normalizeVector(vector)
	if err := s.ensureVectorTable(ctx, len(vector)); err != nil {
		return err
	}
	now := time.Now().UTC()
	if l.CreatedAt.IsZero() {
		l.CreatedAt = now
	}
	if l.UpdatedAt.IsZero() {
		l.UpdatedAt = now
	}
	if l.Count <= 0 {
		l.Count = 1
	}
	keys, _ := json.Marshal(l.ObservedKeys)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	var exists int
	err = tx.QueryRow(`SELECT count FROM lessons WHERE id=?`, l.ID).Scan(&exists)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if err == nil {
		_, err = tx.Exec(`UPDATE lessons SET count=count+1, observed_keys=?, updated_at=? WHERE id=?`, string(keys), now, l.ID)
	} else {
		_, err = tx.Exec(`INSERT INTO lessons(id,tool,class,observed_keys,guidance,count,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?)`, l.ID, l.Tool, l.Class, string(keys), l.Guidance, l.Count, l.CreatedAt, l.UpdatedAt)
	}
	if err != nil {
		return err
	}
	blob, err := sqlite_vec.SerializeFloat32(vector)
	if err != nil {
		return err
	}
	if exists != 0 {
		if _, err := tx.Exec(`DELETE FROM lesson_vectors WHERE id=?`, l.ID); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`INSERT INTO lesson_vectors(id,embedding) VALUES(?,?)`, l.ID, blob); err != nil {
		return err
	}
	return tx.Commit()
}

func lessonEmbeddingText(l Lesson) string {
	return strings.TrimSpace(strings.Join([]string{l.Tool, l.Class, strings.Join(l.ObservedKeys, " "), l.Guidance}, " "))
}

func normalizeVector(vector []float32) {
	var sum float64
	for _, value := range vector {
		sum += float64(value * value)
	}
	if sum == 0 {
		return
	}
	norm := float32(math.Sqrt(sum))
	for i := range vector {
		vector[i] /= norm
	}
}

func (s *LessonStore) All() []Lesson {
	if err := s.init(); err != nil {
		return nil
	}
	rows, err := s.db.Query(`SELECT id,tool,class,observed_keys,guidance,count,created_at,updated_at FROM lessons ORDER BY updated_at DESC`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	return scanLessons(rows)
}

func (s *LessonStore) Delete(ctx context.Context, id string) error {
	if err := s.init(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err := tx.ExecContext(ctx, `DELETE FROM lesson_vectors WHERE id=?`, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM lessons WHERE id=?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

type lessonRows interface {
	Next() bool
	Scan(...any) error
	Err() error
}

func scanLessons(rows lessonRows) []Lesson {
	var lessons []Lesson
	for rows.Next() {
		var lesson Lesson
		var keys string
		if rows.Scan(&lesson.ID, &lesson.Tool, &lesson.Class, &keys, &lesson.Guidance, &lesson.Count, &lesson.CreatedAt, &lesson.UpdatedAt) != nil {
			continue
		}
		_ = json.Unmarshal([]byte(keys), &lesson.ObservedKeys)
		lessons = append(lessons, lesson)
	}
	return lessons
}

// Relevant remains as a compatibility query for callers that explicitly ask
// for exact tool scope. Studio generation uses Semantic instead.
func (s *LessonStore) Relevant(tools []string, limit int) []Lesson {
	all := s.All()
	inUse := map[string]bool{}
	for _, tool := range tools {
		inUse[tool] = true
	}
	var picked []Lesson
	for _, lesson := range all {
		if lesson.Tool == "" || inUse[lesson.Tool] {
			picked = append(picked, lesson)
		}
	}
	sort.SliceStable(picked, func(i, j int) bool {
		if picked[i].Count != picked[j].Count {
			return picked[i].Count > picked[j].Count
		}
		return picked[i].UpdatedAt.After(picked[j].UpdatedAt)
	})
	if limit > 0 && len(picked) > limit {
		picked = picked[:limit]
	}
	return picked
}

// Semantic performs sqlite-vec KNN retrieval and applies a normalized-vector
// confidence threshold before any lesson reaches the prompt.
func (s *LessonStore) Semantic(ctx context.Context, query string, limit int, threshold float64) []Lesson {
	if strings.TrimSpace(query) == "" || s.init() != nil {
		return nil
	}
	if limit <= 0 {
		limit = 5
	}
	if threshold <= 0 {
		threshold = LessonSimilarityThreshold
	}
	vector, err := s.embedder.Embed(ctx, query)
	if err != nil || len(vector) == 0 {
		return nil
	}
	normalizeVector(vector)
	s.mu.Lock()
	if err := s.ensureVectorTable(ctx, len(vector)); err != nil {
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()
	blob, err := sqlite_vec.SerializeFloat32(vector)
	if err != nil {
		return nil
	}
	rows, err := s.db.Query(`SELECT l.id,l.tool,l.class,l.observed_keys,l.guidance,l.count,l.created_at,l.updated_at,v.distance
		FROM lesson_vectors v JOIN lessons l ON l.id=v.id
		WHERE v.embedding MATCH ? AND v.k=? ORDER BY v.distance`, blob, limit)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []Lesson
	for rows.Next() {
		var lesson Lesson
		var keys string
		var distance float64
		if rows.Scan(&lesson.ID, &lesson.Tool, &lesson.Class, &keys, &lesson.Guidance, &lesson.Count, &lesson.CreatedAt, &lesson.UpdatedAt, &distance) != nil {
			continue
		}
		// sqlite-vec reports Euclidean distance. For normalized vectors,
		// cosine similarity is exactly 1-(distance²/2).
		confidence := 1 - (distance*distance)/2
		if confidence < threshold {
			continue
		}
		_ = json.Unmarshal([]byte(keys), &lesson.ObservedKeys)
		out = append(out, lesson)
	}
	return out
}

func LessonsPromptBlock(lessons []Lesson) string {
	if len(lessons) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("\nLESSONS FROM PAST RUNS — real API shapes and fixes observed before; apply them so the flow works the first time:\n")
	for _, l := range lessons {
		sb.WriteString("- ")
		sb.WriteString(learnedData(strings.TrimSpace(l.Guidance)))
		if len(l.ObservedKeys) > 0 {
			sb.WriteString(" (observed keys: " + strings.Join(l.ObservedKeys, ", ") + ")")
		}
		sb.WriteString("\n")
	}
	return sb.String()
}
