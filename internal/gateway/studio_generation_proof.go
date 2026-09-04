package gateway

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	"github.com/soulacy/soulacy/internal/studio"
)

type generationProofRecord struct {
	Owner   string
	Hash    string
	Expires time.Time
}

func draftBaselineHash(d studio.Draft) string {
	d.GenerationProof = ""
	raw, _ := json.Marshal(d)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func (s *Server) issueGenerationProof(owner string, d *studio.Draft) {
	if s == nil || d == nil {
		return
	}
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return
	}
	token := base64.RawURLEncoding.EncodeToString(buf)
	now := time.Now().UTC()
	d.GenerationProof = ""
	record := generationProofRecord{Owner: owner, Hash: draftBaselineHash(*d), Expires: now.Add(24 * time.Hour)}
	s.generationProofMu.Lock()
	for key, old := range s.generationProofs {
		if old.Expires.Before(now) {
			delete(s.generationProofs, key)
		}
	}
	if len(s.generationProofs) >= 2048 {
		for key := range s.generationProofs {
			delete(s.generationProofs, key)
			break
		}
	}
	s.generationProofs[token] = record
	s.generationProofMu.Unlock()
	d.GenerationProof = token
}

func (s *Server) verifyGenerationProof(owner string, d studio.Draft) bool {
	token := strings.TrimSpace(d.GenerationProof)
	if token == "" {
		return false
	}
	s.generationProofMu.Lock()
	record, ok := s.generationProofs[token]
	if ok && (record.Expires.Before(time.Now().UTC()) || record.Owner != owner || record.Hash != draftBaselineHash(d)) {
		ok = false
	}
	if ok {
		delete(s.generationProofs, token)
	}
	s.generationProofMu.Unlock()
	return ok
}
