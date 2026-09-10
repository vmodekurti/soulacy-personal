# Deploy Soulacy Personal with Coolify

Coolify can deploy the published Soulacy Personal container on a VPS you
control. The included Compose file keeps the Soulacy workspace on a persistent
volume and requires authentication before the gateway can start.

1. In Coolify, create a resource from a public Git repository.
2. Enter `https://github.com/vmodekurti/soulacy-personal` and select Docker
   Compose.
3. Set the Compose file to `deploy/coolify/docker-compose.yml`.
4. Add two different secrets in Coolify's environment settings:
   `SOULACY_SERVER_API_KEY` and `SOULACY_AUTH_JWT_SECRET`.
5. Assign a domain to the `soulacy` service on port `18789`, then deploy.

The service pulls `ghcr.io/vmodekurti/soulacy-personal:latest` by default. Pin
`SOULACY_VERSION` to a release tag when you want controlled upgrades.
