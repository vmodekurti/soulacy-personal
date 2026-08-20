package app

import (
	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/config"
)

// scalereport.go — say out loud, at boot, what scale mode does not yet do.
//
// WHY THIS EXISTS. `config.ScaleReplicationBlockers` enumerates the reasons a
// second gateway replica misbehaves. A list nothing reads is worse than no
// list: it looks like the hazard was handled. The operator who set
// deployment.mode: scale, satisfied every validation check, and watched the
// process start cleanly has been told — by the only channel they have — that
// they are ready to scale out.
//
// So the list is printed, once, at the point of no return, and it is printed
// per item. A single summary line ("7 known issues") is skimmable in a way
// seven concrete sentences are not, and the whole failure mode here is
// skimming.
//
// WARN rather than fatal, deliberately. Scale mode with ONE replica is a
// legitimate configuration — it is how an operator stages the infrastructure
// before turning on the second copy — and refusing to boot would make the
// only escape route "stop using scale mode", which loses the queue and the
// artifact store too.

// reportScaleReadiness logs the known multi-replica hazards when the process
// is running in scale mode. It reports nothing in personal or team mode: both
// are single-replica by definition, so the blockers do not apply and printing
// them would train operators to ignore the message.
func reportScaleReadiness(log *zap.Logger, cfg *config.Config) {
	if log == nil || cfg == nil || cfg.DeploymentMode() != config.DeploymentModeScale {
		return
	}
	blockers := config.ScaleReplicationBlockers()
	if len(blockers) == 0 {
		log.Info("scale mode: no known multi-replica blockers remain")
		return
	}
	log.Warn("scale mode is configured but RUNNING A SECOND GATEWAY REPLICA IS NOT YET SAFE; "+
		"each blocker below causes wrong answers rather than errors",
		zap.Int("blocker_count", len(blockers)))
	for i, blocker := range blockers {
		log.Warn("scale replication blocker", zap.Int("index", i+1), zap.String("blocker", blocker))
	}
}
