# Workspace identity and login

Each Team or Scale workspace has its own branded access page and can use a
different OpenID Connect provider. A workspace provider is selected during
initial activation and is then locked.

## Access flow

1. A deployment administrator provisions an organization or workspace.
2. Soulacy creates the workspace in `pending` identity state and returns a
   one-time setup link.
3. The designated workspace administrator opens the link, chooses a provider,
   enters its OIDC settings, and validates discovery and signing keys.
4. Soulacy encrypts any client secret, activates the provider, consumes the
   setup token, and locks the provider and issuer assignment.
5. Members open `/w/{workspace-address}` and continue with the provider shown there.

The generic **Workspace login** action opens `/workspace-login` first. A
returning member enters a short, stable address such as
`customer-support-a1b2c3` or selects a recent workspace remembered by that
browser. Soulacy validates the public workspace configuration and then opens
its branded access page. Internal workspace IDs are not exposed as something
people need to remember. It does not start deployment-wide OAuth because
different workspaces may use different providers.

Supported presets are Google Workspace, Microsoft Entra ID, Okta, Auth0, and
Keycloak. **Custom OIDC** supports any standards-compliant provider whose
discovery document and signing keys validate. GitHub OAuth and Sign in with
Apple are not presented as generic OIDC presets because their provider-specific
flows need separate implementations.

## Provider registration

Register the exact callback URL reported by the setup screen. The provider
must expose OIDC discovery metadata, an authorization endpoint, token endpoint,
JWKS, and an ID-token signing algorithm Soulacy accepts.

Required values:

- issuer URL;
- client ID;
- client secret when the provider requires a confidential client;
- audience when it differs from the client ID;
- `openid`, `profile`, and `email` scopes.

Soulacy keys external identities by issuer and provider subject. Email is used
for invitation matching only when the provider marks it verified.

## Why the assignment is locked

Changing an issuer in place can reinterpret existing provider subjects and
silently attach the wrong people to local accounts. Soulacy therefore treats
the provider and issuer as part of the workspace security boundary. Provider
migration should be an explicit, audited migration procedure rather than a
settings edit.

## Member login and invitations

Successful OIDC authentication does not by itself grant workspace access.
Soulacy resolves an active membership from PostgreSQL on every workspace
request. A new user must receive an invitation for the same verified email
address returned by the provider.

Invitation links open the branded workspace page directly. The one-time token
is carried inside the short-lived OIDC state and accepted only after the
provider returns a verified identity with the invited email address. It is not
placed in the callback URL, access token, or browser history after sign-in.

When login cannot be completed, the browser returns to the branded access page
with a safe message:

- sign-in was cancelled;
- the attempt expired and should be restarted;
- the identity is not authorized for the workspace;
- sign-in failed and the workspace administrator should be contacted.

Internal token, nonce, identity-linking, and database failure details remain in
server logs and are not exposed in the browser.

## Branding

The access screen shows the organization and workspace names and any uploaded
logos. Logos are display-only and do not participate in identity validation.
Verify the URL before authenticating; branding is not a replacement for origin
verification.

## Troubleshooting

| Symptom | Check |
|---|---|
| Callback reports an expired attempt | Start again from the workspace page; do not reuse an old callback tab |
| Account is not authorized | Confirm an active membership or invitation for the provider's verified email |
| Discovery validation fails | Check issuer spelling, HTTPS reachability, discovery metadata, JWKS, and audience |
| Redirect mismatch at the provider | Register the exact callback URL shown by Soulacy |
| Setup link is invalid | Request a newly provisioned link; setup tokens expire and are single-use |

See [Authentication and sessions](auth.md) and
[Workspace members](workspace-members.md).
