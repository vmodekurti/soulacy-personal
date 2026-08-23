# Production execution, KMS, and billing

Team and Scale deliberately refuse to boot with a local credential master key
or an in-process executor. A production deployment needs three independent
trust boundaries.

## Execution plane

Run `soulacy` as the HTTP/auth/database control plane and `soulacy-worker` in a
separate worker node pool. Both use the same TLS-authenticated NATS JetStream,
using a narrowly scoped mounted NATS credentials file or mTLS identity. Workers
do not load `config.yaml`; therefore no database DSN, OIDC secret, Stripe
secret, or gateway API key enters worker memory. Configure the worker with its
narrow environment contract:

```bash
SOULACY_WORKER_NATS_URL=tls://nats.internal:4222
SOULACY_WORKER_NATS_CREDENTIALS=/var/run/secrets/nats/worker.creds
SOULACY_WORKER_NATS_TLS_CA=/var/run/secrets/nats/ca.pem
SOULACY_WORKER_IMAGE=registry.example/execution@sha256:<digest>
SOULACY_WORKER_RUNTIME=runsc
SOULACY_WORKER_COSIGN_KEY=/var/run/keys/execution-image.pub
SOULACY_EXECUTION_ROOT=/workspaces
soulacy-worker
```

Optional `SOULACY_WORKER_*` settings cover stream/subject, mTLS, concurrency,
resource limits, sandbox image, and the policy-proxy network. Give the worker
NATS identity publish/subscribe rights only for execution job/result subjects.

The configured image must be immutable (`@sha256:`), signed, and admitted by
Cosign. `runsc` must be registered with Docker. Ordinary Python jobs always use
`--network none`. Privileged network tools may use only a dedicated network
whose sole route is an authenticated policy proxy; `allowed_egress_hosts` is
passed to that proxy for enforcement and audit.

## External KMS

AWS KMS uses the SDK workload-identity chain (IRSA, ECS task role, or instance
profile) and binds every wrapped data key to its workspace through the KMS
encryption context:

```yaml
credentials:
  kms_provider: awskms
  aws_kms_key_id: alias/soulacy-production
```

Vault Transit supports Kubernetes service-account authentication without a
long-lived Vault token:

```yaml
credentials:
  kms_provider: vault-transit
  hashicorp_addr: https://vault.internal
  hashicorp_mount: transit
  hashicorp_key: soulacy-production
  hashicorp_kubernetes_role: soulacy-gateway
  hashicorp_jwt_path: /var/run/secrets/kubernetes.io/serviceaccount/token
```

Startup performs a wrap/unwrap probe and fails closed. Audit records contain
only provider, operation, key ID, workspace ID, result, and timestamp—never
plaintext or ciphertext. Workspace data-key rotation continues through the
workspace key-rotation API; old wrapped versions remain readable until the
documented re-encryption/retirement step completes.

## Billing and entitlements

```yaml
billing:
  provider: stripe
  stripe_webhook_secret: ${STRIPE_WEBHOOK_SECRET}
  webhook_tolerance: 5m
```

Point Stripe at `POST /webhooks/stripe`. The raw body is HMAC verified, stale
events are rejected, and event IDs are applied transactionally once. Stripe is
an input adapter only: gateway authorization asks the provider-independent
entitlement service whether the workspace may mutate or execute. Payment
failure and subscription deletion block writes/runs immediately while reads
remain available for diagnosis and remediation. A missing entitlement is
treated as an unmetered migration state; operators should reconcile all
production workspaces before enabling paid enforcement.

## Release gates

Run `make security` for tenant-boundary, KMS, entitlement, queue, and execution
unit/race tests. Run `scripts/execution-sandbox-smoke.sh` on a disposable gVisor
worker node with the exact production image; never run an escape test on a
shared worker node.
