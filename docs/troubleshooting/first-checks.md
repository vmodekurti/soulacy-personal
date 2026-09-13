# Find the failing step

Use the smallest harmless test that isolates the problem. Do not start by
reinstalling, deleting a workspace, disabling authentication, or retrying a
write whose outcome is unknown.

## 1. Is the gateway reachable?

Open the same gateway URL in a browser on the affected device. A phone's
`localhost` is not your laptop. Check Wi-Fi/VPN, server power, hostname, and
certificate. A valid certificate on a different hostname is still a mismatch.

On the server, run `sy version` and `sy doctor` in the same workspace and user
environment as the gateway service. If the service reports a port conflict,
identify the process already listening; do not start multiple gateways against
the same data directory.

**If login loads:** continue to authentication. **If it does not:** fix networking
or the server process before troubleshooting the model.

## 2. Can this identity access this agent?

| Response/symptom | Likely area | Safe next step |
|---|---|---|
| 401 or expired access | Missing/expired/revoked credential | Sign in or pair again using the intended gateway |
| 403 | Scope or object permission | Ask the owner for the specific required access, not a shared master key |
| Agent absent or 404 | Wrong ID, hidden resource, or older endpoint | Verify the agent list, current identity, and gateway version |
| 503/unavailable on Safe Undo/files | Feature disabled, unconfigured, or unavailable | Check the feature's operator setup; do not bypass with shell access |

Do not put keys in query strings, screenshots, or bug reports. Gateway keys,
provider keys, JWT signing secrets, and pairing codes are different things.

## 3. Can the model answer a tiny notes-only prompt?

Try the [first-run prompt](../getting-started/quickstart.md) on the intended
agent. Inspect **Model preparation**, **Providers**, and **Activity**.

- Wrong/unknown model: select a model the provider actually offers; save first.
- Provider authentication failure: fix the provider credential, not the gateway key.
- Cost-policy refusal: inspect [Costs](../LLM_COST_CONTROLS.md); do not bypass
  unknown-pricing or hard-budget protection just to force a run.
- Timeout: check provider reachability and a smaller representative task before
  increasing limits. An unavailable local model needs its provider running.
- Wrong answer with a successful run: check evidence and prompt; runtime success
  does not certify answer quality.

## 4. Does the failing tool have the right input and authority?

In Activity, locate the first failed tool, not only the final model reply.
Check exact resource/KB/device IDs, scope, allowlists, and source availability.
For knowledge retrieval, use **Test search** before changing the model.

If a tool needs approval but the run has no interactive confirmation channel,
the default is to deny. `unattended: true` is an explicit operator opt-in with
different consequences—not a routine troubleshooting fix.

## 5. Was the result generated but not delivered?

Separate **generation**, **inbox/channel delivery**, and **notification**:

1. Check the completed output and run status.
2. Check the exact configured destination and delivery receipt.
3. Open the recipient inbox directly.
4. Only then investigate push permission, Focus, APNs/relay setup, or device token.

Manual Schedule **Run** does not prove cron output delivery. Follow the
[briefing test](../use-cases/morning-brief.md).

## Stop immediately for uncertain writes

If an external write may have succeeded but its acknowledgment was lost,
**do not keep pressing retry**. Preserve the job/run ID and inspect the external
resource with the owner. Safe Undo's **Accept observed state** records an
observation; it does not repair or prove the original operation.

## A useful, safe bug report

Copy this checklist and fill it with sanitized information:

```text
Gateway version:
iOS build / browser version:
Feature and screen:
Local network, VPN/tunnel, or public HTTPS:
Steps to reproduce with fictional input:
Expected result:
Actual result and error/status code:
Approximate time and timezone:
Run/job ID (share only in an appropriate private support channel):
Was any external action possibly completed?
```

Never include keys, pairing QR codes, private document text, full environment
files, database snapshots, or unreviewed logs. A support bundle can contain
sensitive context even when redaction is enabled; inspect it before sharing.

For feature-specific errors, continue to [Common failures](common-failures.md).
