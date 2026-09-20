#!/usr/bin/env bash
# Soulacy upgrader — one command that figures out how THIS install was deployed
# and does the right thing.
#
#   curl -fsSL https://raw.githubusercontent.com/vmodekurti/soulacy-personal/main/upgrade.sh | bash
#
# It detects, in order:
#   1. A managed platform (Railway/Render/Fly/Cloud Run/Heroku/K8s) — no shell to
#      act in, so it prints the exact redeploy steps for that platform.
#   2. A Docker/Compose deployment on this host — pulls or rebuilds the image and
#      recreates the container.
#   3. A source checkout — git pull, rebuild, restart.
#   4. A host binary install (~/.local/bin etc.) — self-upgrades with `sy upgrade`
#      and restarts the service.
#
# Safe by design: when it cannot tell, it explains the options instead of
# guessing. Nothing here deletes data or touches your workspace/config.
#
# Overrides:  SOULACY_PORT=18789   SOULACY_UPGRADE_DIR=/path/to/checkout-or-compose

set -euo pipefail

REPO="vmodekurti/soulacy-personal"
PORT="${SOULACY_PORT:-18789}"

GREEN='\033[0;32m'; YELLOW='\033[1;33m'; RED='\033[0;31m'; BOLD='\033[1m'; NC='\033[0m'
ok()   { printf "${GREEN}✓${NC} %s\n" "$*"; }
warn() { printf "${YELLOW}⚠${NC}  %s\n" "$*"; }
err()  { printf "${RED}✗${NC} %s\n" "$*" >&2; }
hdr()  { printf "\n${BOLD}%s${NC}\n" "$*"; }
die()  { err "$*"; exit 1; }

health_ok() { [ "$(curl -s -o /dev/null -w "%{http_code}" --max-time 5 "http://127.0.0.1:${PORT}/healthz" 2>/dev/null)" = "200" ]; }

# ── 1. Managed platform: no shell to act in — guide, never guess ──────────────
detect_managed() {
	local name="" how=""
	if   [ -n "${RAILWAY_ENVIRONMENT:-}${RAILWAY_PROJECT_ID:-}" ]; then name="Railway";      how="In the Railway dashboard, open the service → Deployments → Redeploy. If you pin an image tag, set it to the new version (e.g. ghcr.io/${REPO}:0.1) first."
	elif [ -n "${FLY_APP_NAME:-}" ];                                then name="Fly.io";       how="Run 'fly deploy' from your app directory, or in the dashboard trigger a new release with the new image tag."
	elif [ -n "${RENDER:-}${RENDER_SERVICE_ID:-}" ];               then name="Render";        how="In Render, open the service → Manual Deploy → Deploy latest commit (or set the image tag), which re-pulls and redeploys."
	elif [ -n "${K_SERVICE:-}" ];                                  then name="Google Cloud Run"; how="Deploy a new revision: 'gcloud run deploy <svc> --image ghcr.io/${REPO}:0.1' (or your pinned tag)."
	elif [ -n "${DYNO:-}" ];                                       then name="Heroku";        how="Push a new build (git push heroku) or release the new container image, which restarts the dyno."
	elif [ -n "${KUBERNETES_SERVICE_HOST:-}" ];                    then name="Kubernetes";    how="Bump the image tag in your manifest and 'kubectl apply', or 'kubectl set image deploy/soulacy soulacy=ghcr.io/${REPO}:0.1.NEW' for a rolling update."
	else return 1
	fi
	hdr "Detected a managed platform: ${name}"
	printf "  Soulacy can't upgrade itself here (no writable shell / immutable image).\n"
	printf "  Upgrade by redeploying the newer image:\n\n"
	printf "    ${BOLD}%s${NC}\n\n" "$how"
	printf "  Image: ${BOLD}ghcr.io/%s${NC}  (tags: a version like 0.1.23, the minor 0.1, or latest)\n" "$REPO"
	return 0
}

# ── 2. Docker/Compose on this host ───────────────────────────────────────────
compose_cmd() { if docker compose version >/dev/null 2>&1; then echo "docker compose"; elif command -v docker-compose >/dev/null 2>&1; then echo "docker-compose"; else echo ""; fi; }

find_compose_dir() {
	# Explicit override wins; else the common install location; else cwd.
	for d in "${SOULACY_UPGRADE_DIR:-}" /opt/soulacy /opt/soulacy-personal "$PWD"; do
		[ -n "$d" ] || continue
		for f in docker-compose.yml docker-compose.yaml compose.yml; do
			[ -f "$d/$f" ] && grep -qE "^[[:space:]]*soulacy:" "$d/$f" 2>/dev/null && { echo "$d/$f"; return 0; }
		done
	done
	return 1
}

# service_is_build reports (exit 0) that the soulacy service has a build: stanza.
service_is_build() {
	awk '
		/^([[:space:]]*)soulacy:[[:space:]]*$/ && !seen { match($0, /^[[:space:]]*/); base=RLENGTH; seen=1; next }
		seen {
			if ($0 ~ /[^[:space:]]/) { match($0, /^[[:space:]]*/); ind=RLENGTH; if (ind <= base) exit }
			if ($0 ~ /^[[:space:]]*build:/) { found=1; exit }
		}
		END { exit(found ? 0 : 1) }
	' "$1"
}

upgrade_docker() {
	local dc file dir svc="soulacy"
	dc="$(compose_cmd)"; [ -n "$dc" ] || die "Docker Compose not found."
	file="$(find_compose_dir)" || return 1
	dir="$(dirname "$file")"
	hdr "Detected a Docker/Compose deployment"
	printf "  Compose file: %s\n" "$file"
	cd "$dir"
	# A service with a build: stanza is built from source here; one that only
	# references an image is pulled. service_is_build reads the soulacy block by
	# indentation so the nested build: key isn't mistaken for the next service.
	if service_is_build "$file"; then
		if [ -d "$dir/.git" ]; then
			ok "Build-based service — pulling latest source and rebuilding."
			git -C "$dir" fetch -q --tags origin && git -C "$dir" merge -q --ff-only "@{u}" 2>/dev/null || git -C "$dir" pull -q --ff-only || warn "git pull skipped (detached or dirty tree)"
		else
			warn "Build-based service but no git checkout here — rebuilding current sources."
		fi
		$dc build "$svc"
	else
		ok "Image-based service — pulling the newer image."
		$dc pull "$svc"
	fi
	$dc up -d "$svc"
	printf "  waiting for health"
	for _ in $(seq 1 30); do health_ok && { printf "\n"; ok "Healthy."; break; }; printf "."; sleep 2; done
	health_ok || warn "Not healthy yet — check '$dc logs -f $svc'."
	printf "  Now running: ${BOLD}%s${NC}\n" "$(docker exec "$svc" soulacy --version 2>/dev/null | head -1 || echo '?')"
	return 0
}

# ── 3. Source checkout (git repo, no compose) ────────────────────────────────
find_source_dir() {
	for d in "${SOULACY_UPGRADE_DIR:-}" "$PWD" /opt/soulacy /opt/soulacy-personal; do
		[ -n "$d" ] || continue
		[ -d "$d/.git" ] && [ -f "$d/go.mod" ] && grep -q "soulacy/soulacy" "$d/go.mod" 2>/dev/null && { echo "$d"; return 0; }
	done
	return 1
}

upgrade_source() {
	local dir; dir="$(find_source_dir)" || return 1
	command -v go >/dev/null 2>&1 || { warn "Source checkout at $dir but Go isn't installed — falling back."; return 1; }
	hdr "Detected a source checkout"
	printf "  %s\n" "$dir"; cd "$dir"
	git fetch -q --tags origin && { git merge -q --ff-only "@{u}" 2>/dev/null || git pull -q --ff-only || warn "git pull skipped (detached or dirty tree)"; }
	if [ -d gui ] && command -v npm >/dev/null 2>&1; then (cd gui && npm ci && npm run build); fi
	make all
	restart_host_service || warn "Built new binaries — restart the gateway to run them."
	return 0
}

# ── 4. Host binary install (self-upgrade) ────────────────────────────────────
restart_host_service() {
	if command -v sy >/dev/null 2>&1 && sy daemon status >/dev/null 2>&1; then
		sy daemon stop >/dev/null 2>&1 || true; sy daemon start >/dev/null 2>&1 && { ok "Restarted the gateway service."; return 0; }
	fi
	if command -v launchctl >/dev/null 2>&1 && launchctl list 2>/dev/null | grep -q com.soulacy.soulacy; then
		launchctl kickstart -k "gui/$(id -u)/com.soulacy.soulacy" 2>/dev/null && { ok "Restarted the launchd service."; return 0; }
	fi
	if command -v systemctl >/dev/null 2>&1 && systemctl --user status soulacy >/dev/null 2>&1; then
		systemctl --user restart soulacy && { ok "Restarted the systemd --user service."; return 0; }
	fi
	return 1
}

upgrade_host() {
	command -v sy >/dev/null 2>&1 || return 1
	hdr "Detected a host binary install"
	local out; out="$(sy update check 2>&1 || true)"
	printf "  %s\n" "$(printf '%s' "$out" | head -1)"
	if printf '%s' "$out" | grep -qi "is current"; then ok "Already on the latest release."; return 0; fi
	if sy upgrade; then
		restart_host_service || warn "Upgraded — restart the gateway (e.g. 'soulacy serve') to run the new version."
		return 0
	fi
	warn "In-place upgrade failed (often a permissions issue). Try: sudo sy update install --yes"
	return 0
}

# ── Orchestrate ──────────────────────────────────────────────────────────────
printf "${BOLD}Soulacy upgrade${NC}\n"
if detect_managed; then exit 0; fi
if upgrade_docker; then exit 0; fi
if upgrade_source; then exit 0; fi
if upgrade_host;   then exit 0; fi

hdr "Couldn't determine how Soulacy was deployed here"
cat <<EOF
  Pick the one that matches your install:
    • Host binary:     sy upgrade   (then restart the gateway)
    • Docker/Compose:  cd <compose dir> && docker compose pull soulacy && docker compose up -d soulacy
    • Source checkout: git pull && make all   (then restart)
    • Managed (Railway/Render/Fly/Cloud Run/K8s): redeploy the newer image
      ghcr.io/${REPO}  (tag: a version, the minor 0.1, or latest)
  Or set SOULACY_UPGRADE_DIR=/path/to/your/checkout-or-compose and re-run.
EOF
exit 0
