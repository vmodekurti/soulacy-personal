# Automated AWS deployment

This directory deploys any of Soulacy's three variants with one command. It builds the current checkout, publishes immutable images, signs the execution image with AWS KMS, creates only the infrastructure selected by the operator, waits for the public HTTPS readiness endpoint, and prints the application URL.

## Variant selection

| Variant | Intended use | Components installed |
|---|---|---|
| **Personal** | One trusted operator | Gateway, embedded SQLite/vector storage on encrypted EFS, Docker + gVisor, ALB/ACM/WAF |
| **Team** | Multiple users and workspaces | Personal edge components plus RDS PostgreSQL, credential KMS, private mTLS NATS and a separate isolated worker |
| **Scale** | SaaS/shared production posture | Team components plus Multi-AZ Valkey/Redis and encrypted/versioned S3 artifact storage |

The script prompts for this choice when run from a terminal. Automation and CI should pass it explicitly:

```bash
deploy/aws/deploy.sh --mode personal
deploy/aws/deploy.sh --mode team
deploy/aws/deploy.sh --mode scale
```

## What it creates

- A two-AZ VPC with public ALB subnets and private application/data subnets.
- ACM TLS certificate, Route 53 application record, HTTPS-only Application Load Balancer, and AWS WAF managed protections plus an IP rate limit.
- A private gateway EC2 host. Team and Scale add a separate private execution-worker EC2 host. None accepts SSH; use AWS Systems Manager Session Manager.
- Docker and the pinned gVisor `runsc` runtime on the gateway/worker hosts. Ordinary agent Python runs with no network, read-only rootfs, dropped capabilities, PID/memory/CPU limits, and a signed digest-pinned image.
- Private mTLS NATS JetStream and Multi-AZ RDS PostgreSQL in Team and Scale.
- Multi-AZ ElastiCache Valkey and encrypted, versioned S3 artifact storage in Scale.
- Encrypted EFS workspace storage mounted with TLS in every variant. In Team
  and Scale it is mounted at `/var/lib/soulacy` on both gateway and worker so a
  job's authorized workspace path has the same identity on either side.
- Separate KMS keys for storage and execution-image signing; Team and Scale add workspace-credential KMS.
- Least-purpose EC2 instance roles, IMDSv2-only metadata, S3 public-access blocking, encrypted volumes, and Terraform state in a versioned private S3 bucket.

This is a production-oriented **single gateway / single worker / single NATS node** topology with Multi-AZ managed data stores. It survives an Availability Zone loss at the database and cache layers, but it is not a regionally highly available control plane. Add replicated gateway/worker groups and a three-node NATS cluster before promising a zero-downtime regional SLA.

## Local prerequisites

The deployment machine needs:

- AWS CLI v2 authenticated to the target account with permissions to create the resources above.
- Terraform 1.10 or newer.
- Docker with Buildx.
- Cosign, OpenSSL, `jq`, `curl`, and Git.
- A public Route 53 hosted zone and a hostname inside it.
- An OIDC web client for normal user login (recommended). Its callback URL is `https://YOUR_DOMAIN/api/v1/auth/oidc/callback`.

On macOS with Homebrew:

```bash
brew install awscli terraform cosign jq openssl
brew install --cask docker
```

Log in to AWS and verify the target account before deployment:

```bash
aws sso login --profile YOUR_PROFILE
export AWS_PROFILE=YOUR_PROFILE
aws sts get-caller-identity
```

## Deploy

1. Copy and edit the non-secret variables:

   ```bash
   cp deploy/aws/terraform.tfvars.example deploy/aws/terraform.tfvars
   $EDITOR deploy/aws/terraform.tfvars
   ```

2. If the OIDC provider uses a confidential client, pass its secret through the environment. It is written directly to Secrets Manager and never enters Terraform state:

   ```bash
   export SOULACY_AWS_OIDC_CLIENT_SECRET='replace-me'
   ```

3. Commit the version you intend to deploy, then run:

   ```bash
   deploy/aws/deploy.sh --mode scale
   ```

The optional environment controls are:

```bash
export SOULACY_AWS_REGION=us-east-1
export SOULACY_AWS_NAME=soulacy
export SOULACY_AWS_ENVIRONMENT=production
deploy/aws/deploy.sh --mode team --var-file /absolute/path/to/terraform.tfvars
```

The script is intentionally idempotent. It reuses existing immutable images, signing keys and bootstrap credentials; Team/Scale also reuse their NATS identities. A dirty checkout is rejected; for a disposable test only, explicitly set `SOULACY_AWS_ALLOW_DIRTY=1`.

## Verify and operate

```bash
deploy/aws/status.sh
```

The status script checks the public health endpoint, EC2 status checks, and prints the SSM command for the gateway. From the SSM session:

```bash
sudo journalctl -u soulacy -f
sudo /opt/soulacy/bin/sy doctor
```

`/ready` is end-to-end in Team and Scale: it returns 503 when PostgreSQL, NATS,
or the execution-worker round trip is unavailable. A running queue with a
stopped worker is not considered ready.

Retrieve the bootstrap recovery key only when needed:

```bash
aws secretsmanager get-secret-value \
  --region "$SOULACY_AWS_REGION" \
  --secret-id soulacy/production/bootstrap \
  --query SecretString --output text | jq -r .api_key
```

Treat that key as a break-glass platform credential. Users should authenticate through OIDC and workspace membership.

## Destroy

Deletion protection defaults to true. For an intentional teardown, first set `enable_deletion_protection = false`, run `deploy.sh` to apply that change, then:

```bash
export SOULACY_AWS_DESTROY_CONFIRM=soulacy
deploy/aws/destroy.sh
```

The destroy script deliberately retains remote Terraform state, ECR images, Secrets Manager recovery material, and KMS keys pending their deletion windows. This prevents a single command from irreversibly destroying recovery data. Remove those retained resources separately only after backups and retention obligations are satisfied.

## Important production follow-ups

- Tune the included AWS WAF managed rules and rate limit for the real traffic profile.
- Add CloudWatch log shipping, alarms, RDS/Valkey/NATS backup tests, AWS Backup policies, and budget alerts.
- Replicate gateways and workers across AZs; move NATS to a three-node JetStream cluster for a zero-downtime SLA.
- Establish image-update, KMS/certificate rotation, incident response, restore testing, and Terraform review pipelines.
- Configure LLM providers, Stripe, email, channel credentials, and workspace-specific secrets through Soulacy after the platform is healthy; these are tenant/business integrations, not infrastructure dependencies.
