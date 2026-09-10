# One-click cloud deployment

Soulacy Personal can run as a self-contained, single-operator appliance on AWS,
Azure, Railway, Render, or a Coolify-managed server. AWS and Azure provision the
full production-shaped stack: Soulacy, PostgreSQL, Qdrant, persistent Docker
volumes, and Caddy-managed HTTPS. The PaaS options use a smaller single-container
profile with persistent workspace storage.

Use the deployment buttons on [soulacy.io](https://soulacy.io/#cloud). Review
the cloud provider's estimate and template before creating resources; the
resources are billed to your account.

## AWS

The CloudFormation stack creates its own VPC, public subnet, security group,
encrypted EC2 root disk, Elastic IP, generated Secrets Manager login key, SSM
instance role, and an automatic EC2 recovery alarm. Only ports 80 and 443 are
open. SSH is not exposed.

After the stack reaches `CREATE_COMPLETE`:

1. Open the `SoulacyURL` stack output.
2. Open the Secrets Manager secret named by `LoginSecretARN`.
3. Retrieve the `api_key` field and use it once on the Soulacy login page.

The generated hostname uses the Elastic IP through `sslip.io` so Caddy can
obtain a trusted certificate without requiring a domain during provisioning.
You can later point your own hostname at the Elastic IP and update
`/opt/soulacy/.env` and `/opt/soulacy/Caddyfile` through AWS Systems Manager.

## Azure

The ARM template creates a virtual network, network security group, static
public IP with an Azure DNS name, encrypted Ubuntu VM disk, and a VM extension
that installs the stack. Only ports 80 and 443 are open. SSH is not exposed.

Azure asks for a 24-character-or-longer Soulacy login key during deployment.
Store it in your password manager; secure ARM parameters are not displayed
after deployment. Open the `soulacyURL` deployment output when provisioning
finishes and log in with that key.

## Railway

The [Railway launcher](https://railway.com/new?repo=https%3A%2F%2Fgithub.com%2Fvmodekurti%2Fsoulacy-personal)
imports the public repository and its Dockerfile. The root `railway.json` also
records compatible health and restart defaults for Railway services that still
support config-as-code.
Before exposing the service, set a strong `SOULACY_SERVER_API_KEY`, mount a
persistent volume at `/home/soulacy/.soulacy`, set `PORT=18789`, configure `/`
as the health-check path, and generate a public domain.
Railway detects the container's port from the Dockerfile; the checked-in
configuration supplies the health check and restart policy.

## Render

The [Render launcher](https://render.com/deploy?repo=https%3A%2F%2Fgithub.com%2Fvmodekurti%2Fsoulacy-personal)
uses the root `render.yaml` Blueprint. It creates a Docker web service and a 5 GB
persistent disk, sets the production security profile, and generates separate
Soulacy login and JWT secrets. Save the generated `SOULACY_SERVER_API_KEY` from
the service environment so you can sign in.

## Coolify

Use `deploy/coolify/docker-compose.yml` to run Soulacy on a VPS managed by
Coolify:

1. Create a resource from the public repository
   `https://github.com/vmodekurti/soulacy-personal`.
2. Select Docker Compose and set the file path to
   `deploy/coolify/docker-compose.yml`.
3. Set different random values for `SOULACY_SERVER_API_KEY` and
   `SOULACY_AUTH_JWT_SECRET`.
4. Assign a domain to the `soulacy` service on port `18789`, then deploy.

The Compose file refuses to start with missing secrets and persists the
workspace in a named Docker volume.

## AWS and Azure operations

The deployment lives at `/opt/soulacy` on either VM. Use AWS Systems Manager
or Azure Run Command for administrative access. Common commands are:

```bash
cd /opt/soulacy
sudo docker compose ps
sudo docker compose logs --tail=200 soulacy
sudo docker compose pull
sudo docker compose up -d
```

Docker volumes survive application and VM restarts. Back up the VM disk before
upgrades and before deleting the cloud stack or resource group. Deleting the
VM deletes its attached application data unless you first take a snapshot.

## Source and security boundary

- AWS template: `deploy/aws/cloudformation.yaml`
- Azure template: `deploy/azure/azuredeploy.json`
- Railway configuration: `railway.json`
- Render Blueprint: `render.yaml`
- Coolify Compose file: `deploy/coolify/docker-compose.yml`
- Shared bootstrap: `deploy/common/bootstrap.sh`
- Runtime stack: `deploy/common/docker-compose.cloud.yml`

In the AWS and Azure stack, the gateway, PostgreSQL, and Qdrant are private to
the Docker network and Caddy is the only public service. Railway, Render, and
Coolify expose only the authenticated gateway container. Provider credentials
are configured after login and are not accepted in deployment URLs or committed
templates.
