# 🚀 1-Click Cloud Deployments

Soulacy Personal Edition can be deployed to your preferred cloud provider or PaaS with a single button click.

## Supported 1-Click Platforms

### 1. Railway.com
Deploy a single container instance with auto-TLS domain assignment and a 5GB persistent volume for `/home/soulacy/.soulacy`.

[![Deploy on Railway](https://railway.com/button.svg)](https://railway.com/template/new?template=https%3A%2F%2Fgithub.com%2Fvmodekurti%2Fsoulacy)

- **Cost**: ~$5.00 / month (covered by Railway's Hobby Plan credits).
- **Persistent Volume**: Mounted automatically to `/home/soulacy/.soulacy`.
- **Health Check**: `/ready`

---

### 2. Render.com
Deploy using the official `render.yaml` infrastructure blueprint.

[![Deploy to Render](https://render.com/images/deploy-to-render-button.svg)](https://render.com/deploy?repo=https://github.com/vmodekurti/soulacy)

- **Cost**: ~$7.00 / month (Starter instance + persistent disk).
- **Persistent Volume**: 5GB disk attached at `/home/soulacy/.soulacy`.
- **Health Check**: `/ready`

---

### 3. Coolify (Self-Hosted PaaS)
Deploy on your own VPS or home server using Coolify's open-source PaaS.

1. Open your **Coolify Instance**.
2. Select **+ New Resource** -> **Docker Compose**.
3. Import `deploy/coolify/docker-compose.yml` or point to `https://github.com/vmodekurti/soulacy`.
4. Click **Deploy**.

- **Cost**: $0 (Free / Self-Hosted on your infrastructure).
- **Persistent Volume**: Mapped via Docker Volume `soulacy-data`.

---

## Configuration & Environment Variables

| Variable | Description | Default |
| :--- | :--- | :--- |
| `SOULACY_DEPLOYMENT_MODE` | Deployment mode | `personal` |
| `PORT` | Web GUI & API Gateway port | `1947` |
| `SOULACY_SERVER_API_KEY` | Master API Key for browser login | `sy_secret_12345` |
| `SOULACY_LLM_PROVIDERS_OPENAI_API_KEY` | OpenAI API Key for agents | Optional |
| `SOULACY_LLM_PROVIDERS_ANTHROPIC_API_KEY` | Anthropic API Key for agents | Optional |
