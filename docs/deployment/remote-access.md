# Remote Access From Your Phone

Your gateway listens on `127.0.0.1` (localhost), and home networks sit behind
NAT — so the Soulacy app on your phone can reach it on the same Wi-Fi (once you
allow LAN access) but **not** when you are away. This page sets up secure remote
access so your own gateway works from anywhere.

## One command (recommended: Tailscale)

Run this on the machine that hosts the gateway:

```bash
curl -fsSL https://raw.githubusercontent.com/vmodekurti/soulacy-personal/main/remote.sh | bash
```

It:

1. installs [Tailscale](https://tailscale.com) — a private, WireGuard-encrypted
   mesh VPN — if it is missing;
2. signs the machine into your tailnet (opens a browser once);
3. publishes the gateway into your tailnet with `tailscale serve`. **The gateway
   stays bound to `127.0.0.1`** — nothing is opened on your LAN or the public
   internet, and no router port-forwarding is needed;
4. prints the address and API key to pair your phone with.

On your phone, once: install Tailscale from the App Store, sign into the **same**
account, then in Soulacy → **Settings → add gateway**, enter the address and API
key the script printed. It now works anywhere your phone has internet.

Why Tailscale: it needs no domain, opens no inbound ports, gives a stable
address, encrypts traffic end to end, and is free for personal use. Only your
own signed-in devices can reach the gateway.

To undo:

```bash
tailscale serve --http=18789 off   # stop publishing the gateway
sudo tailscale down                # leave the tailnet
```

## Alternatives

| Method | Account | Domain | Notes |
|--------|---------|--------|-------|
| **Tailscale** (above) | free (SSO) | no | Private mesh VPN; no ports exposed. Recommended. |
| Cloudflare Tunnel (named) | yes | yes | Stable **public** HTTPS URL; good if you also need channel webhooks. See [macOS](macos.md#cloudflare-tunnel-recommended). |
| Cloudflare Quick Tunnel | no | no | `cloudflared tunnel --url http://localhost:18789`; instant public URL but it changes on restart. |
| Router port-forward + Caddy | no | for TLS | Opens a port on your router; only if you specifically want it. |

## Security notes

- Never expose port `18789` directly to the internet. Tailscale (private) and
  Cloudflare Tunnel (TLS, no open ports) both avoid that.
- The gateway requires an API key on any non-loopback path, and each paired
  device holds its own scoped, expiring credential — re-pair from the host to
  renew it.
