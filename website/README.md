# soulacy.io marketing site

Single-page landing site for [soulacy.io](https://soulacy.io/). Plain HTML + Tailwind (via CDN), with no local build step. The page introduces both self-hosted Soulacy Personal and Soulacy Commercial SaaS; the installation instructions are for Personal.

Docs (mkdocs Material) live separately at [docs.soulacy.io](https://docs.soulacy.io/).

## File map

```
website/
├── index.html         ← the entire landing page (hero, security stack, comparison, install, features, footer)
├── brand/             ← approved blue Living Core logo and app-icon assets
├── _headers           ← Cloudflare Pages security + caching headers
├── _redirects         ← Cloudflare Pages redirects (docs subdomain, vanity paths, HN shortlink)
├── robots.txt
├── sitemap.xml
├── install.sh         ← copy of the repo-root install.sh, so curl -fsSL https://soulacy.io/install.sh | bash works
└── README.md          ← this file
```

## Local preview

```bash
cd website
python3 -m http.server 4321 --bind 127.0.0.1
# → open http://127.0.0.1:4321
```

Any static server works. There's no build step.

## Deploy — Cloudflare Pages (recommended)

**One-time setup:**

1. Log in to [Cloudflare Dashboard](https://dash.cloudflare.com/) → Workers & Pages → Create → Pages → Connect to Git.
2. Select `vmodekurti/soulacy-personal`.
3. Build settings:
   - **Framework preset:** None
   - **Build command:** _(leave empty)_
   - **Build output directory:** `website`
   - **Root directory:** _(leave empty)_
4. Save & Deploy. Wait for the deployment to report success before checking its preview URL.
5. Under the deployed project → Custom domains → Set up custom domain → `soulacy.io` and `www.soulacy.io`.
6. Wait for Cloudflare to verify the domains and provision their HTTPS certificates.

**Current production setup:** the `soulacy` Pages project is connected to `vmodekurti/soulacy-personal`. A push to Personal `main` is the normal publishing path. Check the Cloudflare deployment's commit and the live page; a green GitHub job that defers to Git integration is not itself proof that the site deployed. Do not point this public site at the private Commercial repository.

## Deploy — alternatives (if you're not using Cloudflare)

- **Vercel:** import the repo, set output directory to `website`, done.
- **Netlify:** same — `website` as the publish directory.
- **GitHub Pages:** less ideal (already serves docs at docs.soulacy.io); you'd have to set up a second Pages source. Not recommended.

## Refreshing install.sh

The repo root has the canonical `install.sh`. On every deploy, copy it into `website/` so `https://soulacy.io/install.sh` stays in sync:

```bash
cp install.sh website/install.sh
```

Review and commit both files together. Do not add a workflow that commits back to `main` merely to synchronize this copy.

## Iteration notes

- Tailwind is loaded via CDN, which yells in the console. Fine for launch. Post-signal, convert to Astro or ship a built Tailwind bundle.
- Recheck the copy buttons, navigation, phone-width layout, and external documentation links after editing. Do not publish loading-time claims without a dated, reproducible measurement.
- If you add a blog, convert to Astro or 11ty — plain HTML gets painful past ~5 pages.
- Colors are declared in the inline Tailwind config in `index.html` — search `tailwind.config` to tweak.

## Content sources

Every headline claim maps back to:

- Security stack: `docs/PRODUCTIZATION_REVIEW.md` §Cohort F (`internal/trust/`, `internal/injection/`, `internal/intent/`, `internal/securitydoctor/`)
- Recent platform work: `docs/recent-updates.md`, `docs/studio-learning-memory.md`, `docs/LLM_COST_CONTROLS.md`, and the linked operational pages
- Persistent semantic memory: `internal/app/adapters.go`, `internal/memory/vector.go`, and `internal/agentmemory/store.go`
- Human feedback: `internal/gateway/chat_feedback.go` and `internal/learning/feedback.go`
- Comparison chart: `docs/LAUNCH_STRATEGY.md` §3 (with cited URLs per competitor)
- Product editions: `docs/editions.md` (Personal is self-hosted; Commercial is SaaS)
- Binary size and runtime requirements: `docs/deployment/footprint.md` (a measured candidate, not a universal size/RAM/startup guarantee)
- Practical walkthroughs: `docs/use-cases/`

Keep public claims aligned with current code and these guides; historical launch notes are not a product contract. The core Personal runtime does not require Node.js or Python, but optional tools, providers, and integrations may need additional services or software.

Before publishing, run `npm --prefix gui test -- --run` and `make docs-build` from the repository root. The tests guard the approved brand and key product claims; the docs build also checks local links, anchors, and private-artifact exclusions. See `docs/contributing/documentation.md` for the full review checklist.
