<script>
  import { onMount, onDestroy } from 'svelte'

  export let page = 'access'
  export let compact = false

  let open = false
  const help = {
    access: {
      title: 'Choosing how to sign in',
      intro: 'Workspace identity and deployment administration are deliberately separate security boundaries.',
      sections: [
        ['Workspace login', 'Use your organization’s identity provider to enter a workspace where you are an active member.'],
        ['Deployment admin login', 'Use the host deployment key to provision tenants and operate the platform. It does not grant access to workspace data.'],
      ],
      docs: 'https://docs.soulacy.io/configuration/workspace-identity/',
    },
    'admin-login': {
      title: 'Deployment administrator login',
      intro: 'This screen opens the platform control plane, not a customer workspace.',
      sections: [
        ['Which key?', 'Use server.api_key from the host configuration or SOULACY_API_KEY environment variable.'],
        ['What it can do', 'Provision organizations and workspaces, inspect service health, export diagnostics, and restart the gateway.'],
        ['What it cannot do', 'The deployment credential cannot read tenant agents, conversations, runs, knowledge, or secrets.'],
      ],
      docs: 'https://docs.soulacy.io/configuration/platform-administration/',
    },
    'admin-setup': {
      title: 'First-time deployment setup',
      intro: 'Use this wizard once on a new Team or Scale installation. Returning administrators should use /admin.',
      sections: [
        ['Before you begin', 'Prepare PostgreSQL, a stable JWT secret, and the exact callback URL registered with the initial identity provider.'],
        ['What it creates', 'The initial tenant catalog, first organization, first workspace, and its first owner.'],
        ['After setup', 'Restart the gateway, then use the deployment control plane for all later organization and workspace provisioning.'],
      ],
      docs: 'https://docs.soulacy.io/configuration/platform-administration/#first-time-bootstrap',
    },
    'workspace-login': {
      title: 'Workspace access',
      intro: 'Each workspace is bound to one identity-provider configuration when its owner activates the setup link.',
      sections: [
        ['Signing in', 'Continue with the provider shown for this workspace. Your verified identity must have an active membership.'],
        ['No access yet?', 'Ask a workspace owner or administrator for an invitation sent to the same verified email address.'],
        ['Identity setup', 'A one-time setup link lets the initial owner choose and validate the provider. The provider and issuer are then locked.'],
      ],
      docs: 'https://docs.soulacy.io/configuration/workspace-identity/',
    },
    'workspace-discovery': {
      title: 'Find your workspace',
      intro: 'Soulacy must identify your workspace before it can send you to the correct identity provider.',
      sections: [
        ['First time here?', 'Use the invitation link sent by your workspace administrator. It already contains the workspace and your one-time invitation.'],
        ['Returning member?', 'Choose a recent workspace or enter its short address, such as acme-agents. Soulacy remembers successful workspaces in this browser.'],
        ['Why this step exists', 'Different workspaces can use different locked OIDC providers, so there is no safe deployment-wide provider to choose automatically.'],
      ],
      docs: 'https://docs.soulacy.io/configuration/workspace-identity/#access-flow',
    },
    'platform-overview': {
      title: 'Platform overview',
      intro: 'A service-level view of tenant counts, authentication posture, and gateway readiness without reading workspace payloads.',
      sections: [['Start here', 'Confirm the gateway is ready, authentication is required, and tenant counts match expectations before troubleshooting deeper.']],
      docs: 'https://docs.soulacy.io/configuration/platform-administration/#overview',
    },
    'platform-tenants': {
      title: 'Organizations and workspaces',
      intro: 'Provision and suspend tenant boundaries, and designate the first workspace owner without making the deployment administrator a member.',
      sections: [
        ['Organization', 'Creates the customer boundary and its first workspace in one transaction.'],
        ['Workspace', 'Adds an isolated workspace to an active organization and produces a one-time identity setup link.'],
        ['Suspend access', 'Requires a reason and typed-name confirmation. New sign-ins and writes stop; an organization hold affects every child workspace without erasing its individual state.'],
        ['Workspace address', 'Choose a memorable public address when provisioning, or edit an existing address from the workspace row. Addresses must be unique across the deployment.'],
        ['Logos', 'Upload small PNG, JPEG, or WebP logos; they appear on workspace access and selection screens.'],
      ],
      docs: 'https://docs.soulacy.io/configuration/platform-administration/#provisioning',
    },
    'platform-diagnostics': {
      title: 'Platform diagnostics',
      intro: 'Inspect shared dependencies, deployment logs, and a redacted support snapshot before restarting the gateway.',
      sections: [
        ['Gateway logs', 'Raw process logs are visible only here after deployment-key authentication. Workspace users instead see tenant-scoped runs and agent action history.'],
        ['Safe troubleshooting', 'Start with dependency status and the diagnostic snapshot. Restart only when in-flight work can be interrupted.'],
      ],
      docs: 'https://docs.soulacy.io/configuration/platform-administration/#diagnostics',
    },
    'platform-security': {
      title: 'Security boundary',
      intro: 'The deployment administrator operates the service but is not a workspace super-user.',
      sections: [['Support access', 'Workspace data is visible only through an explicit, auditable membership granted by that workspace’s owner.']],
      docs: 'https://docs.soulacy.io/configuration/platform-administration/#security-boundary',
    },
    'platform-audit': {
      title: 'Deployment audit trail',
      intro: 'A searchable record of deployment-key actions and organization or workspace lifecycle changes.',
      sections: [
        ['Scope', 'This trail contains control-plane metadata and identifiers, not conversations, agents, files, secrets, or other workspace payloads.'],
        ['Investigations', 'Search by actor, action, tenant identifier, or request ID. Export the filtered records when handing an incident to another operator.'],
        ['Read accountability', 'Opening the audit trail is itself recorded so access to operational evidence remains accountable.'],
      ],
      docs: 'https://docs.soulacy.io/configuration/platform-administration/#audit-trail',
    },
    'workspace-admin': {
      title: 'Workspace owner settings',
      intro: 'These controls govern only the active workspace and never alter deployment-wide configuration.',
      sections: [
        ['Limits and retention', 'Set tighter workspace budgets, token and concurrency ceilings, and retention windows. Deployment ceilings always remain authoritative.'],
        ['Automation credentials', 'Issue short-lived personal automation credentials, rotate them, or revoke them. Plaintext is shown only once.'],
        ['Audit and exports', 'Search the workspace-scoped audit trail and create expiring, checksummed exports.'],
        ['Deletion', 'Deletion starts a recoverable window. New writes stop immediately; an owner can cancel before the purge deadline.'],
      ],
      docs: 'https://docs.soulacy.io/configuration/workspace-administration/',
    },
  }

  $: content = help[page] || help.access
  function onKeydown(event) { if (event.key === 'Escape') open = false }
  onMount(() => window.addEventListener('keydown', onKeydown))
  onDestroy(() => window.removeEventListener('keydown', onKeydown))
</script>

<button class:compact class="help-button" type="button" on:click={() => open = true} aria-haspopup="dialog">
  <span aria-hidden="true">?</span>{compact ? 'Help' : 'Page help'}
</button>

{#if open}
  <button class="help-scrim" aria-label="Close page help" on:click={() => open = false}></button>
  <div class="help-panel" role="dialog" aria-modal="true" aria-labelledby="page-help-title">
    <header><div><span>PAGE HELP</span><h2 id="page-help-title">{content.title}</h2></div><button on:click={() => open = false} aria-label="Close page help">✕</button></header>
    <p class="intro">{content.intro}</p>
    {#each content.sections as section}
      <section><h3>{section[0]}</h3><p>{section[1]}</p></section>
    {/each}
    <div class="help-links">
      <a href={content.docs} target="_blank" rel="noreferrer">Read the documentation ↗</a>
      <a href="https://soulacy.io" target="_blank" rel="noreferrer">Visit soulacy.io ↗</a>
    </div>
  </div>
{/if}

<style>
  .help-button{display:inline-flex;align-items:center;gap:7px;padding:9px 12px;border:1px solid #ffffff20;border-radius:9px;background:#ffffff08;color:#c8cbe0;font:inherit;font-size:13px;font-weight:700;white-space:nowrap;cursor:pointer}.help-button:hover{border-color:#8e7bff66;background:#8e7bff14;color:#fff}.help-button span{display:grid;place-items:center;width:17px;height:17px;border:1px solid currentColor;border-radius:50%;font-size:11px}.help-button.compact{padding:8px 11px}
  .help-scrim{position:fixed;inset:0;z-index:980;width:100%;height:100%;border:0;background:#050711aa;cursor:default}.help-panel{position:fixed;z-index:981;top:0;right:0;bottom:0;box-sizing:border-box;width:min(430px,92vw);overflow:auto;padding:24px;background:#121626;border-left:1px solid #ffffff1a;box-shadow:-24px 0 70px #0009;color:#eef0ff;text-align:left}.help-panel header{display:flex;align-items:flex-start;gap:16px;padding-bottom:18px;border-bottom:1px solid #ffffff12}.help-panel header>div{flex:1}.help-panel header span{color:#72ddb0;font-size:10px;font-weight:850;letter-spacing:.16em}.help-panel h2{margin:6px 0 0;font-size:23px;letter-spacing:-.03em}.help-panel header button{border:0;background:transparent;color:#8b93ad;font-size:17px;cursor:pointer}.intro{margin:22px 0;color:#abb2c8;font-size:14px;line-height:1.65}.help-panel section{margin-top:20px;padding:15px;border:1px solid #ffffff12;border-radius:11px;background:#090c1688}.help-panel h3{margin:0 0 7px;color:#f3f4ff;font-size:13px}.help-panel section p{margin:0;color:#929bb5;font-size:12px;line-height:1.55}.help-links{display:grid;gap:9px;margin-top:24px}.help-links a{padding:11px 13px;border:1px solid #8e7bff45;border-radius:9px;color:#b8adff;text-decoration:none;font-size:12px;font-weight:750}.help-links a:hover{background:#8e7bff14}
</style>
