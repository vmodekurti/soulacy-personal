# Automated AWS deployment

This directory deploys any Soulacy variant with one command. It builds the current checkout, publishes immutable images, signs the execution image with AWS KMS, creates only the infrastructure selected by the operator, waits for the public HTTPS readiness endpoint, and prints the application URL. Team Lite is a temporary, cost-bounded AWS infrastructure profile; inside Soulacy it remains full `team` mode.

## Easiest Team Lite setup: AWS + Cloudflare + Google

Mac users whose parent domain is hosted by Cloudflare should use the guided
bootstrap instead of configuring Route 53, Terraform, and OIDC independently:

```bash
deploy/aws/team-lite-quickstart.sh
```

On its first run, the wizard asks only for:

1. an AWS CLI profile (it reuses the current/default profile when possible);
2. the AWS Budget notification email;
3. the root domain and desired child label (`soulac.io` + `team` by default);
4. a scoped Cloudflare token with `Zone:Read` and `DNS:Edit`; and
5. a Google Web OAuth client ID and secret.

It creates or reuses a Route 53 public child zone, adds the delegation records
to Cloudflare, writes a secret-free `terraform.tfvars`, and launches the normal
signed Team Lite deployment. It opens the relevant Cloudflare and Google pages
on macOS and prints the exact Google origin and callback URL to copy. The
Cloudflare token remains only in process memory. The Google secret is passed to
`deploy.sh`, which writes it directly to AWS Secrets Manager.

The wizard is rerunnable. It reuses matching DNS configuration and refuses to
replace an existing, different subdomain delegation. Existing
`terraform.tfvars` is backed up before replacement. For non-interactive use,
run `deploy/aws/team-lite-quickstart.sh --help` to see the supported environment
variables.

## Variant selection

| Variant | Intended use | Components installed |
|---|---|---|
| **Personal** | One trusted operator | Gateway, embedded SQLite/vector storage on encrypted EFS, Docker + gVisor, ALB/ACM/WAF |
| **Team Lite** | Small audience or one-month evaluation | Team application mode on burstable compute, Single-AZ PostgreSQL, KMS, private mTLS NATS, separate isolated worker, ALB/ACM/WAF, and an AWS Budget |
| **Team** | Multiple users and workspaces | Personal edge components plus RDS PostgreSQL, credential KMS, private mTLS NATS and a separate isolated worker |
| **Scale** | SaaS/shared production posture | Team components plus Multi-AZ Valkey/Redis and encrypted/versioned S3 artifact storage |

The script prompts for this choice when run from a terminal. Automation and CI should pass it explicitly:

```bash
deploy/aws/deploy.sh --mode personal
deploy/aws/deploy.sh --mode team-lite
deploy/aws/deploy.sh --mode team
deploy/aws/deploy.sh --mode scale
```

## What it creates

- A two-AZ VPC with public ALB subnets and private application/data subnets.
- ACM TLS certificate, Route 53 application record, HTTPS-only Application Load Balancer, and AWS WAF managed protections plus an IP rate limit.
- A gateway EC2 host. Standard profiles place compute in private subnets; Team Lite uses public-subnet egress with no public inbound security-group rules. Team variants add a separate execution-worker EC2 host. None accepts SSH; use AWS Systems Manager Session Manager.
- Docker and the pinned gVisor `runsc` runtime on the gateway/worker hosts. Ordinary agent Python runs with no network, read-only rootfs, dropped capabilities, PID/memory/CPU limits, and a signed digest-pinned image.
- Security-group-confined mTLS NATS JetStream and RDS PostgreSQL in Team variants. Standard Team/Scale use Multi-AZ RDS; Team Lite is Single-AZ.
- Multi-AZ ElastiCache Valkey and encrypted, versioned S3 artifact storage in Scale.
- Encrypted EFS workspace storage mounted with TLS in every variant. In Team
  and Scale it is mounted at `/var/lib/soulacy` on both gateway and worker so a
  job's authorized workspace path has the same identity on either side.
- Separate KMS keys for storage and execution-image signing; Team and Scale add workspace-credential KMS.
- Least-purpose EC2 instance roles, IMDSv2-only metadata, S3 public-access blocking, encrypted volumes, and Terraform state in a versioned private S3 bucket.

This is a production-oriented **single gateway / single worker / single NATS node** topology with Multi-AZ managed data stores. It survives an Availability Zone loss at the database and cache layers, but it is not a regionally highly available control plane. Add replicated gateway/worker groups and a three-node NATS cluster before promising a zero-downtime regional SLA.

## Team Lite: one-month pilot under a $200 credit budget

`--mode team-lite` maps to Soulacy application mode `team`; it does not weaken
RBAC, workspace scoping, PostgreSQL tenancy, KMS credential envelopes, NATS
mTLS, signed execution images, gVisor, or the separate worker boundary. It
changes only the AWS availability and capacity profile:

Docker support remains fully enabled. Bootstrap installs Docker and the pinned
gVisor `runsc` runtime on both gateway and worker; tenant tools and supported
MCP servers continue to execute in constrained containers on the separate
worker rather than in the gateway process.

| Resource | Team Lite setting |
|---|---|
| Gateway / worker | One `t3.small` each, standard CPU credits |
| NATS | One `t3.micro`, 512 MiB container limit and 5 GiB JetStream ceiling |
| PostgreSQL | `db.t4g.micro`, Single-AZ, 20 GiB gp3, one-day backups |
| Worker capacity | One concurrent sandbox job |
| EC2 storage | 20 + 25 + 15 GiB gp3 |
| Egress | Direct public IPv4 on compute with **no inbound public SG rules**; no NAT Gateway |
| Edge | Public HTTPS ALB, ACM certificate, and WAF remain enabled |
| Guardrail | Account-wide AWS Budget, default `$180`, with 50/80% forecast and 100% actual alerts |
| Off-hours | RDS starts at 08:00, EC2 at 08:15, EC2 stops at 22:00, and RDS at 22:10 in `America/Chicago` |

At low traffic in `us-east-1`, the intended infrastructure envelope is roughly
**$90–$150 for 730 hours**. That estimate is not a cap or an AWS guarantee.
Traffic, logs, storage, snapshots, public IPv4 pricing, regional differences,
taxes, and AWS pricing changes can push it higher. LLM/API provider charges are
separate and are not paid by AWS credits.

Use a new account dedicated to this pilot so the account-wide budget measures
Soulacy rather than unrelated resources. AWS Budgets notify; they do not stop
resources. Check **Billing → Cost Explorer** daily and destroy the deployment
as soon as the evaluation ends.

Team Lite deliberately gives up Multi-AZ database failover and sustained CPU
capacity. It is appropriate for a small, temporary audience—not production,
an SLA, high concurrency, or durable workloads that cannot tolerate an
instance/AZ outage.

### Off-hours power control

The Team Lite Terraform profile creates four EventBridge Scheduler jobs. The
database starts first at 08:00, followed by gateway/worker/NATS at 08:15. At
22:00 compute stops first and PostgreSQL follows at 22:10. The IANA timezone
setting follows daylight-saving changes.

Override the defaults during deployment when necessary:

```bash
SOULACY_AWS_BUDGET_EMAIL=you@example.com \
SOULACY_AWS_OFF_HOURS_TIMEZONE=America/New_York \
SOULACY_AWS_OFF_HOURS_START=8 \
SOULACY_AWS_OFF_HOURS_STOP=22 \
deploy/aws/deploy.sh --mode team-lite
```

For an immediate manual override after deployment:

```bash
deploy/aws/power.sh status
deploy/aws/power.sh stop
deploy/aws/power.sh start
```

`start` waits for PostgreSQL before starting the EC2 nodes. `stop` shuts down
the EC2 nodes before PostgreSQL. The next scheduled event still applies after a
manual override.

Stopping is not destroying. EC2 CPU and RDS instance-hour charges pause, and
their automatically assigned public IPv4 addresses are released, but the ALB,
WAF, EFS data, EBS volumes, KMS keys, Route 53, ECR, secrets, and backups remain
available and can continue to incur smaller charges. Destroying these nightly
would delete or repeatedly recreate stateful/security infrastructure and is not
a safe cost optimization. With the default 14-hour daily window, the expected
monthly envelope should be lower than the always-on estimate, but the `$180`
budget remains the operative guardrail.

AWS permits an RDS instance to remain stopped for at most seven consecutive
days before automatically starting it for maintenance. The daily Team Lite
schedule stays within that limitation; a long manual shutdown does not.

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
brew install awscli cosign jq openssl
brew tap hashicorp/tap && brew install hashicorp/tap/terraform
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

For the one-month Team Lite pilot, set the mandatory spend-alert destination
and deploy in one command:

```bash
SOULACY_AWS_BUDGET_EMAIL=you@example.com deploy/aws/deploy.sh --mode team-lite
```

Team Lite defaults the Terraform state and resource environment to `pilot`
rather than `production`, preventing an accidental in-place downsize of a
standard Team deployment. Override it only deliberately with
`SOULACY_AWS_ENVIRONMENT`.

To use a different guardrail (the cost target remains below `$200`):

```bash
SOULACY_AWS_BUDGET_EMAIL=you@example.com \
SOULACY_AWS_MONTHLY_BUDGET_USD=175 \
deploy/aws/deploy.sh --mode team-lite
```

The optional environment controls are:

```bash
export SOULACY_AWS_REGION=us-east-1
export SOULACY_AWS_NAME=soulacy
export SOULACY_AWS_ENVIRONMENT=production
deploy/aws/deploy.sh --mode team --var-file /absolute/path/to/terraform.tfvars
```

The script is intentionally idempotent. It reuses existing immutable images, signing keys and bootstrap credentials; Team/Scale also reuse their NATS identities. A dirty checkout is rejected; for a disposable test only, explicitly set `SOULACY_AWS_ALLOW_DIRTY=1`.

For Team Lite, the script prints the budget/availability warning before making
changes, creates the AWS Budget, and records `DEPLOYMENT_MODE=team` plus
`INFRASTRUCTURE_PROFILE=budget` in `.deployment.env`. Consequently `status.sh`
and `destroy.sh` reproduce the same topology instead of accidentally promoting
the pilot to the standard Team profile.

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

Those retained resources can continue to incur small charges. For a one-month
pilot, inspect and remove ECR images, Secrets Manager secrets, old state object
versions, and KMS keys after the recovery/deletion window. Also verify that the
Route 53 hosted zone is still needed.

## Important production follow-ups

- Tune the included AWS WAF managed rules and rate limit for the real traffic profile.
- Add CloudWatch log shipping, alarms, RDS/Valkey/NATS backup tests, AWS Backup policies, and budget alerts.
- Replicate gateways and workers across AZs; move NATS to a three-node JetStream cluster for a zero-downtime SLA.
- Establish image-update, KMS/certificate rotation, incident response, restore testing, and Terraform review pipelines.
- Configure LLM providers, Stripe, email, channel credentials, and workspace-specific secrets through Soulacy after the platform is healthy; these are tenant/business integrations, not infrastructure dependencies.
