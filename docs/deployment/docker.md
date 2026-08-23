# Docker Deployment

## Quick start

```bash
docker run -d \
  --name soulacy \
  -p 1947:1947 \
  -v $(pwd)/config.yaml:/home/soulacy/.soulacy/config.yaml \
  -v $(pwd)/agents:/home/soulacy/.soulacy/agents \
  -v $(pwd)/memory:/home/soulacy/.soulacy/memory \
  ghcr.io/vmodekurti/soulacy:latest
```

---

## Docker Compose (recommended)

### Basic setup (SQLite)

```yaml title="docker-compose.yml"
version: "3.9"

services:
  soulacy:
    image: ghcr.io/vmodekurti/soulacy:latest
    restart: unless-stopped
    ports:
      - "1947:1947"
    volumes:
      - ./config.yaml:/home/soulacy/.soulacy/config.yaml:ro
      - ./agents:/home/soulacy/.soulacy/agents:ro
      - soulacy_data:/home/soulacy/.soulacy/memory

volumes:
  soulacy_data:
```

### Full stack (Postgres + Qdrant)

```yaml title="docker-compose.yml"
version: "3.9"

services:
  soulacy:
    image: ghcr.io/vmodekurti/soulacy:latest
    restart: unless-stopped
    ports:
      - "1947:1947"
    volumes:
      - soulacy_data:/home/soulacy/.soulacy
    depends_on:
      postgres:
        condition: service_healthy
      qdrant:
        condition: service_started
    environment:
      - SOULACY_STORAGE_BACKEND=postgres
      - SOULACY_STORAGE_POSTGRES_DSN=postgres://soulacy:secret@postgres:5432/soulacy?sslmode=disable
      - SOULACY_VECTOR_BACKEND=qdrant
      - SOULACY_VECTOR_URL=http://qdrant:6333
      - SOULACY_VECTOR_COLLECTION=soulacy_memory

  postgres:
    image: postgres:16-alpine
    restart: unless-stopped
    environment:
      POSTGRES_USER: soulacy
      POSTGRES_PASSWORD: secret
      POSTGRES_DB: soulacy
    volumes:
      - pg_data:/var/lib/postgresql/data
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U soulacy"]
      interval: 5s
      timeout: 5s
      retries: 5

  qdrant:
    image: qdrant/qdrant:latest
    restart: unless-stopped
    volumes:
      - qdrant_data:/qdrant/storage

volumes:
  soulacy_data:
  pg_data:
  qdrant_data:
```

---

## Environment variable overrides

All config values can be set via environment variables (useful for secrets in CI/CD). Note that Soulacy uses single underscores (`_`) as separators:

```bash
SOULACY_SERVER_API_KEY=sy_secret
SOULACY_LLM_PROVIDERS_OPENAI_API_KEY=sk-...
SOULACY_CHANNELS_TELEGRAM_TOKEN=1234:AAH...
```

---

## Health check

```bash
curl http://localhost:1947/api/v1/health
# {"status":"ok","version":"0.1.0"}
```

Docker healthcheck in Compose:

```yaml
healthcheck:
  test: ["CMD", "curl", "-fs", "http://localhost:1947/api/v1/health"]
  interval: 30s
  timeout: 10s
  retries: 3
```

---

## Using a reverse proxy (nginx)

```nginx title="nginx.conf"
server {
    listen 443 ssl;
    server_name yourdomain.com;

    ssl_certificate /etc/letsencrypt/live/yourdomain.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/yourdomain.com/privkey.pem;

    location / {
        proxy_pass http://soulacy:1947;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_read_timeout 120s;    # allow time for long LLM responses
    }
}
```
