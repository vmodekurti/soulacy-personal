package gateway

import (
	"context"

	"go.uber.org/zap"

	mobilechan "github.com/soulacy/soulacy/internal/channels/mobile"
	"github.com/soulacy/soulacy/internal/memory"
)

// migrateLegacyCompanionIdentities hands rows created under a legacy
// companion key id to the owner. Before managed keys carried a subject, a
// paired phone authenticated as its key id, so its device row, node
// registration, deliveries and adaptive memory were keyed by that id. The
// auth engine now maps such keys to "admin"; without this the phone's own
// rows would refuse it as "registered to another user" and its memory
// would look empty. Idempotent: a key with an explicit subject is skipped
// and rows already under "admin" are untouched.
func (s *Server) migrateLegacyCompanionIdentities(ctx context.Context) {
	if s == nil || s.apiKeyStore == nil {
		return
	}
	keys, err := s.apiKeyStore.List(ctx, true)
	if err != nil {
		s.log.Warn("legacy companion identity migration skipped", zap.Error(err))
		return
	}
	var moved int64
	for _, k := range keys {
		if k.Name != companionKeyName || k.Subject != "" {
			continue
		}
		if store := mobilechan.DefaultStore(); store != nil {
			n, err := store.ReassignUser(ctx, "personal", k.ID, "admin")
			if err != nil {
				s.log.Warn("legacy companion identity migration: mobile", zap.String("key", k.ID), zap.Error(err))
			}
			moved += n
		}
		if local, ok := s.engine.AdaptiveMemory().(*memory.LocalAdaptive); ok && local != nil && local.Store() != nil {
			n, err := local.Store().ReassignOwner(ctx, s.adaptiveWorkspace(), k.ID, "admin")
			if err != nil {
				s.log.Warn("legacy companion identity migration: memory", zap.String("key", k.ID), zap.Error(err))
			}
			moved += n
		}
	}
	if moved > 0 {
		s.log.Info("legacy companion identities moved to the owner", zap.Int64("rows", moved))
	}
}
