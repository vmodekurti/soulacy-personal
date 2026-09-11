# 1-Click Deployment on Coolify

Coolify is an open-source, self-hosted Railway alternative for deploying Soulacy on your own VPS or home server.

## Deploying on Coolify

1. Open your **Coolify Instance Dashboard**.
2. Click **+ Add Project** -> **+ New Resource** -> **Docker Compose**.
3. Paste the contents of `deploy/coolify/docker-compose.yml` or enter the repository URL `https://github.com/vmodekurti/soulacy`.
4. Set your environment variables:
   - `SOULACY_SERVER_API_KEY`: Your secure API key for Soulacy GUI login.
   - `SOULACY_LLM_PROVIDERS_OPENAI_API_KEY`: Your OpenAI API key.
5. Click **Deploy**. Coolify automatically provisions a persistent volume for `/home/soulacy/.soulacy` and configures SSL reverse proxying on your domain.
