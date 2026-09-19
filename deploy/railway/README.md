# Deploying Soulacy on Railway

`railway.json` (at the repo root) builds Soulacy from the Dockerfile. **You must
also attach a persistent volume**, or every redeploy will wipe your data.

## Required: a persistent volume

Railway volumes are created per-service (dashboard or CLI), not in
`railway.json`. Add one mounted at the workspace root:

- **Mount path:** `/home/soulacy/.soulacy`
- **Size:** 5 GB is plenty to start.

Dashboard: service → **Settings → Volumes → New Volume**, mount path
`/home/soulacy/.soulacy`. CLI: `railway volume add --mount-path /home/soulacy/.soulacy`.

Everything Soulacy persists — config, agents, memory, secrets, skills — lives
under that path, so with the volume attached a redeploy keeps all of it.

## Confirm it's safe

Soulacy checks this at startup and in `sy doctor`. Without a persistent volume it
logs, and reports:

> workspace is NOT on persistent storage; data will be lost on the next redeploy

If you see that, attach the volume above and redeploy **once more** before you
rely on the instance (the warning means the current data is not yet durable).

## Required env

Set `SOULACY_SERVER_HOST=0.0.0.0`, a strong `SOULACY_SERVER_API_KEY`, and (for
the hosted login flow) `SOULACY_AUTH_MODE=jwt` with a random
`SOULACY_AUTH_JWT_SECRET`. Upgrades: **Deployments → Redeploy** (see
[Upgrades](../../docs/deployment/upgrades.md)).
