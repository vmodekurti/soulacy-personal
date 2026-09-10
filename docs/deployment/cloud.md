# One-click cloud deployment

Soulacy Personal can run as a self-contained, single-operator appliance on AWS
or Azure. Both launchers provision the full production-shaped stack: Soulacy,
PostgreSQL, Qdrant, persistent Docker volumes, and Caddy-managed HTTPS.

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

## Operations

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
- Shared bootstrap: `deploy/common/bootstrap.sh`
- Runtime stack: `deploy/common/docker-compose.cloud.yml`

The gateway, PostgreSQL, and Qdrant are private to the Docker network. Caddy is
the only public service. Provider credentials are configured after login and
are not accepted in deployment URLs or committed templates.

