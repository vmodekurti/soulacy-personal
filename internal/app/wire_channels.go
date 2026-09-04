package app

// wire_channels.go — channel adapter registration extracted from App.Run
// (Story ARCH-4). Construction is registry-routed (Story E10); the host keeps
// the config-shape handling (single-bot vs multi-bot lists, adapter ids,
// system-agent guard). Behavior is preserved verbatim from the original
// inline block.

import (
	"fmt"
	"os"
	"path/filepath"

	"go.uber.org/zap"

	"github.com/soulacy/soulacy/internal/channels"
	wachan "github.com/soulacy/soulacy/internal/channels/whatsapp"
	wawebchan "github.com/soulacy/soulacy/internal/channels/whatsappweb"
	"github.com/soulacy/soulacy/internal/config"
	"github.com/soulacy/soulacy/internal/runtime"
)

// registerChannels registers every configured channel adapter onto chanReg.
// It returns the concrete WhatsApp adapter (needed by the gateway for its
// webhook routes) or nil when WhatsApp is not configured. The caller invokes
// chanReg.StartAll afterwards.
func (a *App) registerChannels(chanCfg map[string]map[string]any, chanReg *channels.Registry, loader *runtime.Loader, ws config.Paths) *wachan.Adapter {
	log := a.log

	// ── Telegram ─────────────────────────────────────────────────────────────
	// Top-level config is the canonical/default adapter ID ("telegram"). It is
	// commonly used as the default scheduled-output sender. Bot rows are
	// additional agent mappings and get distinct IDs when a top-level bot exists.
	if tgCfg, ok := chanCfg["telegram"]; ok {
		if enabled, _ := tgCfg["enabled"].(bool); enabled {
			hasDefaultBot := false
			if token, _ := tgCfg["token"].(string); token != "" {
				hasDefaultBot = true
				agentID, _ := tgCfg["agent_id"].(string)
				outboundOnly, _ := tgCfg["outbound_only"].(bool)
				if agentID == "" || outboundOnly || bindingDecision("telegram", agentID, "telegram", tgCfg, loader, log) {
					if tg, cerr := buildChannel("telegram", "telegram", tgCfg, log); cerr != nil {
						log.Warn("telegram channel skipped", zap.Error(cerr))
					} else {
						chanReg.Register(tg)
						log.Info("telegram default bot registered",
							zap.String("adapter_id", "telegram"),
							zap.String("agent_id", agentID),
							zap.Bool("outbound_only", outboundOnly || agentID == ""))
					}
				}
			}
			if rawBots, hasBots := tgCfg["bots"]; hasBots {
				if botList, ok := rawBots.([]any); ok {
					for i, rawBot := range botList {
						botMap, ok := rawBot.(map[string]any)
						if !ok {
							continue
						}
						token, _ := botMap["token"].(string)
						agentID, _ := botMap["agent_id"].(string)
						botName, _ := botMap["bot_name"].(string)
						if token == "" {
							continue
						}
						adapterID := "telegram"
						if hasDefaultBot || i > 0 {
							suffix := sanitizeID(agentID)
							if suffix == "" {
								suffix = sanitizeID(botName)
							}
							if suffix == "" {
								suffix = fmt.Sprintf("%d", i+1)
							}
							adapterID = "telegram-" + suffix
						}
						outboundOnly, _ := botMap["outbound_only"].(bool)
						if agentID != "" && !outboundOnly {
							if !bindingDecision(adapterID, agentID, "telegram", bindingCfgWithInheritedConsent(tgCfg, botMap), loader, log) {
								continue
							}
						}
						tg, cerr := buildChannel("telegram", adapterID, botMap, log)
						if cerr != nil {
							log.Warn("telegram bot skipped", zap.String("adapter_id", adapterID), zap.Error(cerr))
							continue
						}
						chanReg.Register(tg)
						log.Info("telegram bot registered",
							zap.String("adapter_id", adapterID),
							zap.String("agent_id", agentID),
							zap.String("bot_name", botName))
					}
				}
			}
		}
	}

	// ── Discord ───────────────────────────────────────────────────────────────
	// Same dual-mode as Telegram: single-bot (legacy) or multi-bot via `bots:`.
	if dsCfg, ok := chanCfg["discord"]; ok {
		if enabled, _ := dsCfg["enabled"].(bool); enabled {
			if rawBots, hasBots := dsCfg["bots"]; hasBots {
				if botList, ok := rawBots.([]any); ok {
					for i, rawBot := range botList {
						botMap, ok := rawBot.(map[string]any)
						if !ok {
							continue
						}
						token, _ := botMap["token"].(string)
						agentID, _ := botMap["agent_id"].(string)
						botName, _ := botMap["bot_name"].(string)
						if token == "" {
							continue
						}
						if !bindingDecision(adapterIDForLog("discord", i, agentID), agentID, "discord", bindingCfgWithInheritedConsent(dsCfg, botMap), loader, log) {
							continue
						}
						adapterID := "discord"
						if i > 0 {
							adapterID = "discord-" + sanitizeID(agentID)
						}
						ds, cerr := buildChannel("discord", adapterID, botMap, log)
						if cerr != nil {
							log.Warn("discord bot skipped", zap.String("adapter_id", adapterID), zap.Error(cerr))
							continue
						}
						chanReg.Register(ds)
						log.Info("discord bot registered",
							zap.String("adapter_id", adapterID),
							zap.String("agent_id", agentID),
							zap.String("bot_name", botName))
					}
				}
			} else {
				token, _ := dsCfg["token"].(string)
				agentID, _ := dsCfg["agent_id"].(string)
				if token != "" {
					if bindingDecision("discord", agentID, "discord", dsCfg, loader, log) {
						if ds, cerr := buildChannel("discord", "discord", dsCfg, log); cerr != nil {
							log.Warn("discord channel skipped", zap.Error(cerr))
						} else {
							chanReg.Register(ds)
						}
					}
				}
			}
		}
	}

	// ── Slack ─────────────────────────────────────────────────────────────────
	// Same dual-mode as Telegram: single-bot (legacy) or multi-bot via `bots:`.
	if slCfg, ok := chanCfg["slack"]; ok {
		if enabled, _ := slCfg["enabled"].(bool); enabled {
			if rawBots, hasBots := slCfg["bots"]; hasBots {
				if botList, ok := rawBots.([]any); ok {
					for i, rawBot := range botList {
						botMap, ok := rawBot.(map[string]any)
						if !ok {
							continue
						}
						botToken, _ := botMap["bot_token"].(string)
						appToken, _ := botMap["app_token"].(string)
						agentID, _ := botMap["agent_id"].(string)
						botName, _ := botMap["bot_name"].(string)
						if botToken == "" || appToken == "" {
							continue
						}
						if !bindingDecision(adapterIDForLog("slack", i, agentID), agentID, "slack", bindingCfgWithInheritedConsent(slCfg, botMap), loader, log) {
							continue
						}
						adapterID := "slack"
						if i > 0 {
							adapterID = "slack-" + sanitizeID(agentID)
						}
						sl, cerr := buildChannel("slack", adapterID, botMap, log)
						if cerr != nil {
							log.Warn("slack bot skipped", zap.String("adapter_id", adapterID), zap.Error(cerr))
							continue
						}
						chanReg.Register(sl)
						log.Info("slack bot registered",
							zap.String("adapter_id", adapterID),
							zap.String("agent_id", agentID),
							zap.String("bot_name", botName))
					}
				}
			} else {
				botToken, _ := slCfg["bot_token"].(string)
				appToken, _ := slCfg["app_token"].(string)
				agentID, _ := slCfg["agent_id"].(string)
				if botToken != "" && appToken != "" {
					if bindingDecision("slack", agentID, "slack", slCfg, loader, log) {
						if sl, cerr := buildChannel("slack", "slack", slCfg, log); cerr != nil {
							log.Warn("slack channel skipped", zap.Error(cerr))
						} else {
							chanReg.Register(sl)
						}
					}
				}
			}
		}
	}

	// WhatsApp is webhook-driven (Meta pushes to us), but it still needs to
	// send replies via the Graph API. Register it in chanReg so StartAll()
	// wires the shared inbox and Send() can route replies to it.
	var waAdapter *wachan.Adapter
	if waCfg, ok := chanCfg["whatsapp"]; ok {
		if enabled, _ := waCfg["enabled"].(bool); enabled {
			// app_secret (HMAC verification on inbound webhook POSTs,
			// PRODUCTION_AUDIT → CRIT/Security) is consumed by the factory.
			phoneNumberID, _ := waCfg["phone_number_id"].(string)
			accessToken, _ := waCfg["access_token"].(string)
			verifyToken, _ := waCfg["verify_token"].(string)
			agentID, _ := waCfg["agent_id"].(string)
			if phoneNumberID != "" && accessToken != "" && verifyToken != "" {
				if bindingDecision("whatsapp", agentID, "whatsapp", waCfg, loader, log) {
					if wa, cerr := buildChannel("whatsapp", "", waCfg, log); cerr != nil {
						log.Warn("whatsapp channel skipped", zap.Error(cerr))
					} else {
						// The gateway needs the concrete adapter for its
						// webhook routes; the factory contract returns the
						// channel.Adapter interface.
						waAdapter, _ = wa.(*wachan.Adapter)
						chanReg.Register(wa) // StartAll will call wa.Start()
					}
				}
			}
		}
	}

	// WhatsApp Web is an experimental QR-linked channel backed by a Node
	// sidecar (Baileys). Kept separate from the official Meta Cloud API
	// adapter above so deployments make an explicit tradeoff.
	if waWebCfg, ok := chanCfg["whatsapp_web"]; ok {
		if enabled, _ := waWebCfg["enabled"].(bool); enabled {
			command, _ := waWebCfg["command"].(string)
			args := channels.ParseStringList(waWebCfg["args"])
			sessionDir, _ := waWebCfg["session_dir"].(string)
			accountID, _ := waWebCfg["account_id"].(string)
			agentID, _ := waWebCfg["agent_id"].(string)
			activation := channels.ActivationFromConfig(waWebCfg, true)
			if command == "" {
				command = "node"
			}
			if sessionDir == "" {
				sessionDir = filepath.Join(ws.Data, "whatsapp-web")
			}
			// Installed binaries have no repo checkout: when args are
			// absent or point at a missing script, materialise the
			// embedded sidecar into the session dir (the pair API installs
			// the Baileys dependency next to it).
			if agentID != "" {
				_, statErr := os.Stat(firstOr(args, ""))
				switch {
				case len(args) == 0 || statErr != nil:
					if sp, serr := wawebchan.EnsureSidecarScript(sessionDir); serr != nil {
						log.Warn("whatsapp_web sidecar script unavailable", zap.Error(serr))
					} else {
						args = []string{sp}
					}
				case filepath.Base(args[0]) == wawebchan.SidecarScriptName:
					// Managed script: re-sync so binary upgrades ship
					// sidecar fixes even though the file already exists.
					if _, serr := wawebchan.EnsureSidecarScript(filepath.Dir(args[0])); serr != nil {
						log.Warn("whatsapp_web sidecar script refresh failed", zap.Error(serr))
					}
				}
			}
			if len(args) > 0 && agentID != "" {
				if bindingDecision("whatsapp_web", agentID, "whatsapp_web", waWebCfg, loader, log) {
					waWeb := wawebchan.New("whatsapp_web", command, args, sessionDir, agentID, accountID, activation, log)
					chanReg.Register(waWeb)
					log.Warn("experimental WhatsApp Web channel enabled",
						zap.String("agent_id", agentID),
						zap.String("account_id", accountID),
						zap.String("trigger_phrase", activation.TriggerPhrase),
						zap.Bool("ignore_groups", activation.IgnoreGroups))
				}
			}
		}
	}

	// ── Third-party registry channels (E10/E12) ──────────────────────────────
	// Any channels.<key> block whose key isn't handled above resolves through
	// the SDK factory registry under that key — this is how flavored-binary
	// drivers (docs/CUSTOM_DISTRIBUTIONS.md) wire from config with no host
	// changes. Unknown names warn and skip; the gateway always boots.
	for chID, chCfg := range chanCfg {
		switch chID {
		case "telegram", "discord", "slack", "whatsapp", "whatsapp_web", "http":
			continue
		}
		if enabled, _ := chCfg["enabled"].(bool); !enabled {
			continue
		}
		agentID, _ := chCfg["agent_id"].(string)
		if !bindingDecision(chID, agentID, chID, chCfg, loader, log) {
			continue
		}
		adp, cerr := buildChannel(chID, chID, chCfg, log)
		if cerr != nil {
			log.Warn("channel skipped (no registered factory or bad config)",
				zap.String("channel", chID), zap.Error(cerr))
			continue
		}
		chanReg.Register(adp)
		log.Info("registry channel registered",
			zap.String("channel", chID), zap.String("agent_id", agentID))
	}

	return waAdapter
}

func bindingCfgWithInheritedConsent(parent, child map[string]any) map[string]any {
	if child == nil {
		return child
	}
	if _, hasOwn := child["accept_privileged_exposure"]; hasOwn || parent == nil {
		return child
	}
	if v, ok := parent["accept_privileged_exposure"]; ok {
		cp := make(map[string]any, len(child)+1)
		for k, val := range child {
			cp[k] = val
		}
		cp["accept_privileged_exposure"] = v
		return cp
	}
	return child
}
