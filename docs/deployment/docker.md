# Docker deployment

## Supported topology

The repository's Docker and Compose quick starts run **Personal mode**. They
are appropriate for one trusted operator and deliberately do not mount a host
container-runtime socket into Soulacy.

| Mode | Gateway packaging | Execution plane | Supported path |
|---|---|---|---|
| Personal | Binary or container | Local/container execution owned by the operator | `docker-compose.lite.yml` or `docker-compose.yml` |
| Team | Binary, container, or Kubernetes | Separate signed-image OCI worker on a dedicated node | AWS deployment or an equivalent operator-built worker plane |
| Scale | Replicated containers/Kubernetes | Autoscaled, multi-node hardened worker pool | AWS/Kubernetes production architecture |

PostgreSQL makes Personal Compose more durable; it does not make it Team mode.

## Personal quick start

```bash
cp .env.example .env
# Set SOULACY_API_KEY and, for the full stack, POSTGRES_PASSWORD.
docker compose -f docker-compose.lite.yml up --build -d
curl -fsS http://localhost:1947/ready
```

Use the full Personal stack when you want PostgreSQL and Qdrant:

```bash
docker compose up --build -d
curl -fsS http://localhost:1947/ready
```

Both files persist `/home/soulacy/.soulacy`, explicitly set
`SOULACY_DEPLOYMENT_MODE=personal`, and use `/ready` for container health.

## Team and Scale

Do not convert Personal Compose to Team or Scale by setting one environment
variable. Multi-user operation requires all of the following:

- JWT/OIDC authentication and a bootstrap administration key;
- PostgreSQL and an external KMS;
- TLS-authenticated durable NATS;
- `executor.backend: worker`;
- at least one live `soulacy-worker` on a dedicated execution node;
- a digest-pinned, Cosign-verified execution image;
- gVisor `runsc` or an equivalent hardened OCI runtime;
- the same encrypted workspace filesystem mounted at the same absolute path on
  gateways and workers;
- Redis-backed shared limits and shared artifacts in Scale.

The gateway's `/ready` endpoint checks PostgreSQL, the queue, and a real worker
round trip. It returns 503 when the queue is alive but no worker is consuming.

Use [the automated AWS deployment](https://github.com/vmodekurti/soulacy/blob/main/deploy/aws/README.md) for the packaged
Team/Scale path. For another cloud, reproduce the boundary described in
[Production execution](../configuration/production-runtime.md).

## Why `docker.sock` is prohibited

Do not mount `/var/run/docker.sock`, a rootless Docker socket, containerd,
CRI-O, or a Kubernetes administrative credential into the gateway. A runtime
API can create containers with host mounts and is therefore an administrative
control plane, not a sandbox.

Do not use privileged Docker-in-Docker for Team or Scale either. It creates a
second daemon with a broad kernel boundary, complicated persistent storage, and
an unnecessary breakout surface.

Workers may control the runtime on their dedicated execution nodes. Disposable
workload containers never receive the runtime socket. Compromise is therefore
contained to a replaceable execution node rather than the gateway/database
control plane.

## Configuration and secrets

Environment variables use the `SOULACY_` prefix:

```bash
SOULACY_SERVER_API_KEY=sy_secret
SOULACY_LLM_PROVIDERS_OPENAI_API_KEY=sk-...
SOULACY_CHANNELS_TELEGRAM_TOKEN=1234:AAH...
```

Use a secret manager or mounted secret file in production. Never bake provider,
OIDC, database, or workspace credentials into an image.

## Health and readiness

`GET /ready` is the unauthenticated, non-diagnostic readiness endpoint intended
for Docker, load balancers, and Kubernetes. It returns only a status and an
actionable HTTP code. Authenticated operators can use `GET /api/v1/ready` for
dependency detail.

```yaml
healthcheck:
  test: ["CMD", "curl", "-fs", "http://localhost:1947/ready"]
  interval: 15s
  timeout: 5s
  retries: 5
```

## Reverse proxy

```nginx
server {
    listen 443 ssl;
    server_name yourdomain.com;

    ssl_certificate /etc/letsencrypt/live/yourdomain.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/yourdomain.com/privkey.pem;

    location / {
        proxy_pass http://soulacy:1947;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-Proto https;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_read_timeout 120s;
    }
}
```
