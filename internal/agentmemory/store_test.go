package agentmemory_test

import (
	"testing"
	"time"

	"github.com/soulacy/soulacy/internal/agentmemory"
)

// MEM-08: write 3 episodic records, read back in recency order, assert most
// recent is first.
func TestEpisodicStore_WriteAndReadRecent(t *testing.T) {
	dir := t.TempDir()
	store := agentmemory.NewEpisodicStore(dir)

	agentID := "test-agent"

	records := []agentmemory.Record{
		{AgentID: agentID, Content: "oldest task", Timestamp: time.Now().Add(-2 * time.Hour)},
		{AgentID: agentID, Content: "middle task", Timestamp: time.Now().Add(-1 * time.Hour)},
		{AgentID: agentID, Content: "newest task", Timestamp: time.Now()},
	}

	for _, r := range records {
		if err := store.Write(r); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}

	got, err := store.ReadRecent(agentID, 3)
	if err != nil {
		t.Fatalf("ReadRecent: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 records, got %d", len(got))
	}

	// Most recent must be first.
	if got[0].Content != "newest task" {
		t.Errorf("expected first record to be 'newest task', got %q", got[0].Content)
	}
	if got[2].Content != "oldest task" {
		t.Errorf("expected last record to be 'oldest task', got %q", got[2].Content)
	}
}

// Scenario C: MaxEpisodic=2 returns only 2 records even when 3 are stored.
func TestCompositeStore_RetrieveMaxEpisodic(t *testing.T) {
	dir := t.TempDir()
	store := agentmemory.NewCompositeStore(dir, nil)

	agentID := "research-agent"
	for i := 0; i < 3; i++ {
		err := store.Write(agentmemory.Record{
			AgentID:   agentID,
			Type:      agentmemory.MemoryTypeEpisodic,
			Content:   "task record",
			Timestamp: time.Now().Add(time.Duration(i) * time.Second),
		})
		if err != nil {
			t.Fatalf("Write: %v", err)
		}
	}

	result, err := store.Retrieve(agentmemory.RetrieveQuery{
		AgentID:     agentID,
		MaxEpisodic: 2,
	})
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}

	if len(result.EpisodicSummary) != 2 {
		t.Errorf("expected 2 episodic records, got %d", len(result.EpisodicSummary))
	}
}

// Scenario C: BuildContextBlock output contains "## Recent task history".
func TestBuildContextBlock_ContainsHeader(t *testing.T) {
	dir := t.TempDir()
	store := agentmemory.NewCompositeStore(dir, nil)

	agentID := "research-agent"
	_ = store.Write(agentmemory.Record{
		AgentID: agentID,
		Type:    agentmemory.MemoryTypeEpisodic,
		Content: "some task completed",
	})

	result, err := store.Retrieve(agentmemory.RetrieveQuery{AgentID: agentID, MaxEpisodic: 5})
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}

	block := agentmemory.BuildContextBlock(result)
	if block == "" {
		t.Fatal("BuildContextBlock returned empty string")
	}
	if !contains(block, "## Recent task history") {
		t.Errorf("expected '## Recent task history' in block, got:\n%s", block)
	}
}

// TestProceduralStore: write rules, read back, verify content matches.
func TestProceduralStore_UpdateAndRead(t *testing.T) {
	dir := t.TempDir()
	store := agentmemory.NewProceduralStore(dir)

	agentID := "writing-agent"
	rules := "# Rules\n- Always cite sources\n- Be concise\n"

	if err := store.Update(agentID, rules); err != nil {
		t.Fatalf("Update: %v", err)
	}

	got := store.Read(agentID)
	if got != rules {
		t.Errorf("expected %q, got %q", rules, got)
	}
}

// TestResultToEpisodicRecord: verify the record is well-formed.
func TestResultToEpisodicRecord(t *testing.T) {
	rec := agentmemory.ResultToEpisodicRecord(
		"research-agent",
		"What is RAG?",
		"RAG stands for Retrieval-Augmented Generation.",
		[]string{"rag", "research"},
	)

	if rec.AgentID != "research-agent" {
		t.Errorf("expected AgentID 'research-agent', got %q", rec.AgentID)
	}
	if rec.Type != agentmemory.MemoryTypeEpisodic {
		t.Errorf("expected type 'episodic', got %q", rec.Type)
	}
	if rec.ID == "" {
		t.Error("expected non-empty ID")
	}
	if rec.Timestamp.IsZero() {
		t.Error("expected non-zero timestamp")
	}
	if !contains(rec.Content, "What is RAG?") {
		t.Errorf("expected content to contain task input, got %q", rec.Content)
	}
	if len(rec.Tags) != 2 {
		t.Errorf("expected 2 tags, got %d", len(rec.Tags))
	}
}

// TestWrite_RejectOversizedContent: content > 32KB must be rejected.
func TestWrite_RejectOversizedContent(t *testing.T) {
	dir := t.TempDir()
	store := agentmemory.NewCompositeStore(dir, nil)

	bigContent := make([]byte, 33*1024)
	for i := range bigContent {
		bigContent[i] = 'x'
	}

	err := store.Write(agentmemory.Record{
		AgentID: "test-agent",
		Type:    agentmemory.MemoryTypeEpisodic,
		Content: string(bigContent),
	})
	if err == nil {
		t.Error("expected error for oversized content, got nil")
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && containsStr(s, sub))
}

func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// ─── EpisodicStore: empty content rejected ───────────────────────────────────

func TestEpisodicStore_Write_EmptyContentRejected(t *testing.T) {
	dir := t.TempDir()
	store := agentmemory.NewEpisodicStore(dir)
	err := store.Write(agentmemory.Record{AgentID: "agent", Content: ""})
	if err == nil {
		t.Error("expected error for empty content, got nil")
	}
}

// ─── EpisodicStore: max=0 returns all records ─────────────────────────────────

func TestEpisodicStore_ReadRecent_MaxZeroReturnsAll(t *testing.T) {
	dir := t.TempDir()
	store := agentmemory.NewEpisodicStore(dir)
	agentID := "all-agent"
	for i := 0; i < 5; i++ {
		if err := store.Write(agentmemory.Record{AgentID: agentID, Content: "record"}); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	got, err := store.ReadRecent(agentID, 0)
	if err != nil {
		t.Fatalf("ReadRecent: %v", err)
	}
	if len(got) != 5 {
		t.Errorf("expected 5 records with max=0, got %d", len(got))
	}
}

// ─── EpisodicStore: no file → nil (not error) ────────────────────────────────

func TestEpisodicStore_ReadRecent_NoFile(t *testing.T) {
	dir := t.TempDir()
	store := agentmemory.NewEpisodicStore(dir)
	got, err := store.ReadRecent("nonexistent-agent", 5)
	if err != nil {
		t.Fatalf("expected no error for missing file, got %v", err)
	}
	if got != nil {
		t.Errorf("expected nil result for missing file, got %v", got)
	}
}

// ─── EpisodicStore: multi-agent isolation ────────────────────────────────────

func TestEpisodicStore_MultiAgentIsolation(t *testing.T) {
	dir := t.TempDir()
	store := agentmemory.NewEpisodicStore(dir)

	_ = store.Write(agentmemory.Record{AgentID: "agent-a", Content: "a's record"})
	_ = store.Write(agentmemory.Record{AgentID: "agent-b", Content: "b's record"})

	recA, _ := store.ReadRecent("agent-a", 10)
	recB, _ := store.ReadRecent("agent-b", 10)

	if len(recA) != 1 || recA[0].Content != "a's record" {
		t.Errorf("agent-a: got %v", recA)
	}
	if len(recB) != 1 || recB[0].Content != "b's record" {
		t.Errorf("agent-b: got %v", recB)
	}
}

// ─── EpisodicStore: auto-assigns ID and sets Type ─────────────────────────────

func TestEpisodicStore_Write_AutoIDAndType(t *testing.T) {
	dir := t.TempDir()
	store := agentmemory.NewEpisodicStore(dir)
	_ = store.Write(agentmemory.Record{AgentID: "ag", Content: "auto fields"})

	records, err := store.ReadRecent("ag", 1)
	if err != nil {
		t.Fatalf("ReadRecent: %v", err)
	}
	if len(records) == 0 {
		t.Fatal("expected 1 record")
	}
	if records[0].ID == "" {
		t.Error("expected auto-assigned ID")
	}
	if records[0].Type != agentmemory.MemoryTypeEpisodic {
		t.Errorf("expected Type=episodic, got %q", records[0].Type)
	}
	if records[0].Timestamp.IsZero() {
		t.Error("expected non-zero Timestamp")
	}
}

// ─── ProceduralStore: update overwrites ──────────────────────────────────────

func TestProceduralStore_UpdateOverwrites(t *testing.T) {
	dir := t.TempDir()
	store := agentmemory.NewProceduralStore(dir)
	agentID := "overwrite-agent"

	_ = store.Update(agentID, "old rules")
	_ = store.Update(agentID, "new rules")

	got := store.Read(agentID)
	if got != "new rules" {
		t.Errorf("expected 'new rules', got %q", got)
	}
}

func TestProceduralStore_Read_NoFileReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	store := agentmemory.NewProceduralStore(dir)
	got := store.Read("never-written")
	if got != "" {
		t.Errorf("expected empty string for missing file, got %q", got)
	}
}

// ─── InMemoryVectorStore: write and search ────────────────────────────────────

func TestInMemoryVectorStore_WriteAndSearch(t *testing.T) {
	vs := agentmemory.NewInMemoryVectorStore()
	agentID := "vec-agent"

	_ = vs.Write(agentmemory.Record{AgentID: agentID, Content: "retrieval augmented generation"})
	_ = vs.Write(agentmemory.Record{AgentID: agentID, Content: "totally unrelated document"})

	results, _ := vs.Search(agentID, "retrieval augmented", 5)
	if len(results) == 0 {
		t.Fatal("expected at least one result for matching query")
	}
	if results[0].Content != "retrieval augmented generation" {
		t.Errorf("top result: got %q", results[0].Content)
	}
}

func TestInMemoryVectorStore_Search_AgentIsolation(t *testing.T) {
	vs := agentmemory.NewInMemoryVectorStore()
	_ = vs.Write(agentmemory.Record{AgentID: "a1", Content: "relevant data"})
	_ = vs.Write(agentmemory.Record{AgentID: "a2", Content: "relevant data"})

	results, _ := vs.Search("a1", "relevant", 10)
	for _, r := range results {
		if r.AgentID != "a1" {
			t.Errorf("search returned record for wrong agent %q", r.AgentID)
		}
	}
}

func TestInMemoryVectorStore_Search_MaxLimit(t *testing.T) {
	vs := agentmemory.NewInMemoryVectorStore()
	agentID := "limit-agent"
	for i := 0; i < 10; i++ {
		_ = vs.Write(agentmemory.Record{AgentID: agentID, Content: "keyword content " + string(rune('a'+i))})
	}
	results, _ := vs.Search(agentID, "keyword", 3)
	if len(results) > 3 {
		t.Errorf("Search should honour max=3, got %d", len(results))
	}
}

func TestInMemoryVectorStore_Search_NoMatch(t *testing.T) {
	vs := agentmemory.NewInMemoryVectorStore()
	_ = vs.Write(agentmemory.Record{AgentID: "ag", Content: "unrelated content here"})
	results, _ := vs.Search("ag", "zebra giraffe", 5)
	if len(results) != 0 {
		t.Errorf("expected 0 results for non-matching query, got %d", len(results))
	}
}

func TestInMemoryVectorStore_Write_SetsTypeAndID(t *testing.T) {
	vs := agentmemory.NewInMemoryVectorStore()
	_ = vs.Write(agentmemory.Record{AgentID: "ag", Content: "content"})
	results, _ := vs.Search("ag", "content", 1)
	if len(results) == 0 {
		t.Fatal("expected one result")
	}
	if results[0].Type != agentmemory.MemoryTypeSemantic {
		t.Errorf("expected Type=semantic, got %q", results[0].Type)
	}
	if results[0].ID == "" {
		t.Error("expected non-empty ID")
	}
}

// ─── CompositeStore: write all three types ────────────────────────────────────

func TestCompositeStore_Write_SemanticType(t *testing.T) {
	dir := t.TempDir()
	store := agentmemory.NewCompositeStore(dir, agentmemory.NewInMemoryVectorStore())
	agentID := "sem-agent"

	err := store.Write(agentmemory.Record{
		AgentID: agentID,
		Type:    agentmemory.MemoryTypeSemantic,
		Content: "knowledge chunk about RAG",
	})
	if err != nil {
		t.Fatalf("Write semantic: %v", err)
	}

	result, err := store.Retrieve(agentmemory.RetrieveQuery{AgentID: agentID, TaskInput: "RAG", MaxSemantic: 5})
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if len(result.SemanticChunks) == 0 {
		t.Error("expected at least one semantic chunk after write")
	}
}

func TestCompositeStore_Write_ProceduralType(t *testing.T) {
	dir := t.TempDir()
	store := agentmemory.NewCompositeStore(dir, nil)
	agentID := "proc-agent"

	err := store.Write(agentmemory.Record{
		AgentID: agentID,
		Type:    agentmemory.MemoryTypeProcedural,
		Content: "# Rules\n- Be helpful",
	})
	if err != nil {
		t.Fatalf("Write procedural: %v", err)
	}

	rules := store.ProceduralRules(agentID)
	if rules != "# Rules\n- Be helpful" {
		t.Errorf("expected procedural rules, got %q", rules)
	}
}

// ─── CompositeStore: ClearEpisodic ───────────────────────────────────────────

func TestCompositeStore_ClearEpisodic(t *testing.T) {
	dir := t.TempDir()
	store := agentmemory.NewCompositeStore(dir, nil)
	agentID := "clear-agent"

	for i := 0; i < 3; i++ {
		_ = store.Write(agentmemory.Record{AgentID: agentID, Type: agentmemory.MemoryTypeEpisodic, Content: "record"})
	}

	if err := store.ClearEpisodic(agentID); err != nil {
		t.Fatalf("ClearEpisodic: %v", err)
	}

	records, err := store.EpisodicRecords(agentID, 0)
	if err != nil {
		t.Fatalf("EpisodicRecords after clear: %v", err)
	}
	if len(records) != 0 {
		t.Errorf("expected 0 records after clear, got %d", len(records))
	}
}

func TestCompositeStore_ClearEpisodic_Idempotent(t *testing.T) {
	dir := t.TempDir()
	store := agentmemory.NewCompositeStore(dir, nil)
	// Clear for an agent that never wrote anything should not error.
	if err := store.ClearEpisodic("ghost-agent"); err != nil {
		t.Errorf("ClearEpisodic nonexistent: %v", err)
	}
}

// ─── CompositeStore: EpisodicRecords ─────────────────────────────────────────

func TestCompositeStore_EpisodicRecords(t *testing.T) {
	dir := t.TempDir()
	store := agentmemory.NewCompositeStore(dir, nil)
	agentID := "records-agent"

	for i := 0; i < 5; i++ {
		_ = store.Write(agentmemory.Record{AgentID: agentID, Type: agentmemory.MemoryTypeEpisodic, Content: "task"})
	}

	records, err := store.EpisodicRecords(agentID, 3)
	if err != nil {
		t.Fatalf("EpisodicRecords: %v", err)
	}
	if len(records) != 3 {
		t.Errorf("expected 3 records (capped), got %d", len(records))
	}
}

// ─── CompositeStore: UpdateProcedural ────────────────────────────────────────

func TestCompositeStore_UpdateProcedural(t *testing.T) {
	dir := t.TempDir()
	store := agentmemory.NewCompositeStore(dir, nil)
	agentID := "updater-agent"

	if err := store.UpdateProcedural(agentID, "## Rules\n- Rule 1"); err != nil {
		t.Fatalf("UpdateProcedural: %v", err)
	}

	result, err := store.Retrieve(agentmemory.RetrieveQuery{AgentID: agentID})
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if result.ProceduralRules != "## Rules\n- Rule 1" {
		t.Errorf("procedural rules mismatch: got %q", result.ProceduralRules)
	}
}

// ─── CompositeStore: Retrieve default maxes ───────────────────────────────────

func TestCompositeStore_Retrieve_DefaultMaxEpisodic(t *testing.T) {
	dir := t.TempDir()
	store := agentmemory.NewCompositeStore(dir, nil)
	agentID := "default-max"

	for i := 0; i < 10; i++ {
		_ = store.Write(agentmemory.Record{AgentID: agentID, Type: agentmemory.MemoryTypeEpisodic, Content: "rec"})
	}

	// MaxEpisodic=0 → defaults to 5
	result, err := store.Retrieve(agentmemory.RetrieveQuery{AgentID: agentID, MaxEpisodic: 0})
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if len(result.EpisodicSummary) != 5 {
		t.Errorf("default MaxEpisodic: expected 5, got %d", len(result.EpisodicSummary))
	}
}

// ─── BuildContextBlock: all three sections ────────────────────────────────────

func TestBuildContextBlock_AllSections(t *testing.T) {
	result := agentmemory.RetrieveResult{
		EpisodicSummary: []agentmemory.Record{
			{Timestamp: time.Now(), Content: "completed task X"},
		},
		SemanticChunks: []agentmemory.Record{
			{Content: "relevant knowledge"},
		},
		ProceduralRules: "## Rules\n- Do the right thing",
	}

	block := agentmemory.BuildContextBlock(result)

	for _, want := range []string{"## Recent task history", "## Relevant knowledge", "## Operating rules", "completed task X", "relevant knowledge", "Do the right thing"} {
		if !containsStr(block, want) {
			t.Errorf("BuildContextBlock missing %q:\n%s", want, block)
		}
	}
}

func TestBuildContextBlock_Empty(t *testing.T) {
	block := agentmemory.BuildContextBlock(agentmemory.RetrieveResult{})
	if block != "" {
		t.Errorf("expected empty block for empty result, got %q", block)
	}
}

func TestBuildContextBlock_OnlyProcedural(t *testing.T) {
	result := agentmemory.RetrieveResult{
		ProceduralRules: "only rules here",
	}
	block := agentmemory.BuildContextBlock(result)
	if !containsStr(block, "## Operating rules") {
		t.Errorf("expected operating rules header, got %q", block)
	}
	if containsStr(block, "## Recent task history") {
		t.Errorf("should not have episodic header when empty, got %q", block)
	}
}

// ─── ResultToEpisodicRecord: truncates oversized content ──────────────────────

func TestResultToEpisodicRecord_TruncatesOversized(t *testing.T) {
	bigOutput := make([]byte, 35*1024)
	for i := range bigOutput {
		bigOutput[i] = 'b'
	}
	rec := agentmemory.ResultToEpisodicRecord("ag", "task", string(bigOutput), nil)
	if len(rec.Content) > 32*1024 {
		t.Errorf("expected content truncated to 32KB, got %d bytes", len(rec.Content))
	}
}

// ─── CompositeStore: nil vector store disables semantic writes ───────────────

func TestCompositeStore_NilVectorStoreRequiresProductionWiring(t *testing.T) {
	dir := t.TempDir()
	// Passing nil vectorStore should not panic and should create a default.
	store := agentmemory.NewCompositeStore(dir, nil)
	if store == nil {
		t.Fatal("NewCompositeStore returned nil")
	}
	// Semantic writes must not silently degrade to keyword matching.
	err := store.Write(agentmemory.Record{
		AgentID: "ag", Type: agentmemory.MemoryTypeSemantic, Content: "test",
	})
	if err == nil {
		t.Fatal("Write semantic without sqlite-vec should fail")
	}
}
