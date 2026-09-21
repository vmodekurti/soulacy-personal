# Remote Access From Your Phone

A gateway listening on `127.0.0.1` (localhost) is reachable only from the host
Mac. An iPhone cannot reach that listener over Wi-Fi. Choose one of these paths:

- keep the gateway on localhost and publish it through Tailscale (recommended
  when the phone should work both at home and away); or
- bind the gateway to a private address for direct access on a trusted home LAN.

Never forward the gateway's port directly from your router to the internet.

## Home-LAN-only access

Use this path when the Mac Studio and iPhone will communicate only while they
are on the same trusted network. Reserve a private address for the Mac in the
router's DHCP settings, then configure the gateway with that address:

```yaml
server:
  host: 192.168.1.20       # the Mac's reserved private address
  port: 18789
  api_key: "sy_<strong-random-value>"
  tls_auto: true
  discovery:
    enabled: true
    interface: en0         # use the Mac's active Ethernet or Wi-Fi interface
    hostname: soulacy-studio.local
    name: Mac Studio
```

Restart Soulacy and allow incoming connections if the macOS firewall asks. The
phone must be on the same non-guest network; guest Wi-Fi and client-isolation
settings commonly block device-to-device traffic. Bonjour additionally needs
multicast UDP 5353, and the gateway needs TCP 18789.

Open **Mobile → Pair a phone** on the Mac and scan the QR in the iOS app. Leave
`server.public_url` empty for this direct path so the pairing page can select
and verify the reachable private address. With automatic TLS active, the QR
carries the gateway key fingerprint; iOS pins it and stores the issued device
credential in Keychain. **Find nearby gateways** can fill in the address, but
Bonjour is discovery only and does not authenticate or pair the phone. See
[Nearby gateways on iOS](../configuration/server.md#nearby-gateways-on-ios-bonjour)
for interface and hostname requirements.

This LAN listener is visible to other devices on that network, so keep the API
key enabled and use a trusted, access-controlled Wi-Fi network.

## Access at home and away

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
