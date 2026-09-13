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
2. Open the `LoginSecretConsoleURL` output (or find the secret named by
   `LoginSecretARN`) and reveal the `api_key` field.
3. Use that key on the Soulacy login page and save it in a password manager.
4. Choose **Mobile → Pair a device** and scan the short-lived QR code with
   Soulacy for iOS. Normal phone use relies on the scoped paired credential.

The generated hostname uses the Elastic IP through `sslip.io` so Caddy can
obtain a trusted certificate without requiring a domain during provisioning.
The stack reports `CREATE_COMPLETE` only after the gateway answers HTTP inside
the VM, so a completed stack means the application started, not just the VM.

Accounts on the AWS Free Tier plan may be limited to `t3.small`; larger types
fail at instance creation with a Free Tier eligibility error.
You can later point your own hostname at the Elastic IP and update
`/opt/soulacy/.env` and `/opt/soulacy/Caddyfile` through AWS Systems Manager.

## Azure

The ARM template creates a virtual network, network security group, static
public IP with an Azure DNS name, encrypted Ubuntu VM disk, and a VM extension
that installs the stack. Only ports 80 and 443 are open. SSH is not exposed.

Azure asks for a 24-character-or-longer Soulacy login key during deployment.
Store it in your password manager; secure ARM parameters are not displayed
after deployment. Open the `soulacyURL` deployment output when provisioning
finishes and log in with that key. Then choose **Mobile → Pair a device** and
scan the short-lived QR code with Soulacy for iOS.

The VM's local administrator password is generated per deployment with
`newGuid()` and never shown; SSH is closed, so administer the VM with Azure
Run Command. The default size is `Standard_B2s_v2` (2 vCPU, 8 GiB); choose a
`D` series size for heavier agents.

## Railway

The [Soulacy Personal Railway template](https://railway.com/deploy/soulacy-personal?utm_medium=integration&utm_source=button&utm_campaign=soulacy-personal)
imports the public repository and its Dockerfile, attaches persistent storage,
enables public networking and health checks, and generates two independent
secrets for every deployment. No configuration fields are required.

After Railway reports the service online:

1. Open the service's **Variables** tab.
2. Reveal and copy `SOULACY_SERVER_API_KEY` (the `sy_...` value). Do not use
   `SOULACY_AUTH_JWT_SECRET`; that is an internal signing secret.
3. Open the generated public domain and sign in with the gateway key.
4. Choose **Mobile → Pair a device** and scan the short-lived QR code with the
   iOS app. The QR contains a single-use pairing code, not the permanent key.

Keep the generated gateway key unsealed so the project owner can retrieve it.
Railway account permissions protect the Variables page. Do not publish it in
logs, screenshots, support tickets, or URL query parameters.

The root `railway.json` records compatible health and restart defaults for
Railway services that support config-as-code.
Railway injects its assigned runtime port as `PORT`. The container entrypoint
maps that value to `SOULACY_SERVER_PORT`; outside Railway, Soulacy continues to
use port `18789`. The checked-in configuration supplies the health check and
restart policy. The entrypoint also repairs the ownership of a newly mounted
Railway volume before dropping to the unprivileged `soulacy` user, so the
gateway can initialize its workspace without running as root.

## Pair an iPhone after any cloud deployment

AWS, Azure, and Railway differ only in how the owner retrieves the initial
gateway key. Once signed in, use **Mobile → Pair a device** in the Soulacy web
workspace. Pairing codes expire after two minutes, work once, and redeem for a
scoped mobile credential stored by iOS in Keychain. Never put the permanent
gateway API key directly in a QR code or a public post-deployment URL.

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

## Published templates and versions

The website buttons do not read the templates from the `main` branch. The
release workflow publishes both templates to the public bucket
`soulacy-public-deploy-633654243571` after the release image is pushed, with
`ImageVersion`/`imageVersion` and `ReleaseRef`/`releaseRef` defaulted to that
exact release. A one-click deployment therefore always pairs a tagged image
with the bootstrap assets from the same tag. Each release also keeps an
immutable copy under `releases/<tag>/`. The publish job needs the
`AWS_DEPLOY_ASSETS_ROLE_ARN` repository variable, an IAM role trusted by
GitHub's OIDC provider for `refs/tags/v*` of this repository, with `s3:PutObject`
on the bucket; without it the job skips with a warning and the previously
published templates remain live.

Pull requests that touch `deploy/**` run `scripts/test-cloud-deploy.sh`,
`cfn-lint` on the CloudFormation template, and `arm-ttk` on the ARM template.

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
