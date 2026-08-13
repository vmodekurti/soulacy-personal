# soulacy.io marketing site

Single-page landing site for [soulacy.io](https://soulacy.io/). Plain HTML + Tailwind (via CDN) — no build step, no npm dependency, deployable to any static host in seconds.

Docs (mkdocs Material) live separately at [docs.soulacy.io](https://docs.soulacy.io/).

## File map

```
website/
├── index.html         ← the entire landing page (hero, security stack, comparison, install, features, footer)
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
python3 -m http.server 4321
# → open http://localhost:4321
```

Any static server works. There's no build step.

## Deploy — Cloudflare Pages (recommended)

**One-time setup:**

1. Log in to [Cloudflare Dashboard](https://dash.cloudflare.com/) → Workers & Pages → Create → Pages → Connect to Git.
2. Select `vmodekurti/soulacy`.
3. Build settings:
   - **Framework preset:** None
   - **Build command:** _(leave empty)_
   - **Build output directory:** `website`
   - **Root directory:** _(leave empty)_
4. Save & Deploy. First build takes ~10 seconds since nothing is built.
5. Under the deployed project → Custom domains → Set up custom domain → `soulacy.io` and `www.soulacy.io`.
6. Cloudflare will provision Let's Encrypt certs and route the apex + www.

**On every push to `main`:** Cloudflare rebuilds automatically (fast — it's just copying files). Preview deploys fire on every PR.

## Deploy — alternatives (if you're not using Cloudflare)

- **Vercel:** import the repo, set output directory to `website`, done.
- **Netlify:** same — `website` as the publish directory.
- **GitHub Pages:** less ideal (already serves docs at docs.soulacy.io); you'd have to set up a second Pages source. Not recommended.

## Refreshing install.sh

The repo root has the canonical `install.sh`. On every deploy, copy it into `website/` so `https://soulacy.io/install.sh` stays in sync:

```bash
cp install.sh website/install.sh
```

Or wire it into CI (`.github/workflows/website-sync-install.yml`):

```yaml
name: Sync install.sh to website
on:
  push:
    branches: [main]
    paths: [install.sh]
jobs:
  sync:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - run: cp install.sh website/install.sh
      - run: |
          git config user.email "actions@github.com"
          git config user.name "GitHub Actions"
          git add website/install.sh
          git diff --cached --quiet || git commit -m "chore(website): sync install.sh"
          git push
```

## Iteration notes

- Tailwind is loaded via CDN, which yells in the console. Fine for launch. Post-signal, convert to Astro or ship a built Tailwind bundle.
- No JS beyond the copy-button clipboard call. Loads in ~100 ms.
- If you add a blog, convert to Astro or 11ty — plain HTML gets painful past ~5 pages.
- Colors are declared in the inline Tailwind config in `index.html` — search `tailwind.config` to tweak.

## Content sources

Every headline claim maps back to:

- Security stack: `docs/PRODUCTIZATION_REVIEW.md` §Cohort F (`internal/trust/`, `internal/injection/`, `internal/intent/`, `internal/securitydoctor/`)
- Recent platform work: `docs/recent-updates.md`, `docs/studio-learning-memory.md`, `docs/LLM_COST_CONTROLS.md`, and the linked operational pages
- Persistent semantic memory: `internal/app/adapters.go`, `internal/memory/vector.go`, and `internal/agentmemory/store.go`
- Human feedback: `internal/gateway/chat_feedback.go` and `internal/learning/feedback.go`
- Comparison chart: `docs/LAUNCH_STRATEGY.md` §3 (with cited URLs per competitor)
- "What Soulacy is NOT": `docs/LAUNCH_STRATEGY.md` §5

If you edit the site, edit the memo/review too so the two don't drift.
