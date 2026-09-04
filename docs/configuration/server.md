# Server Configuration

Controls the HTTP server that exposes the REST API and channel webhooks.

## Reference

```yaml
server:
  host: 127.0.0.1      # bind address (default: 127.0.0.1)
  port: 18789          # port to listen on (default: 18789)
  api_key: "sy_..."    # master server API key (required)
  read_timeout: 30s    # HTTP read timeout
  write_timeout: 60s   # HTTP write timeout
  idle_timeout: 120s   # keep-alive idle timeout
  tls:
    enabled: false
    cert_file: /etc/ssl/certs/soulacy.crt
    key_file:  /etc/ssl/private/soulacy.key
```

## Options

### `host`

IP address or hostname to bind. Use `0.0.0.0` to accept connections on all interfaces, or `127.0.0.1` to accept localhost only.

### `port`

TCP port the HTTP server listens on. Default: `18789`.

### `api_key`

The master API key for the server. Clients must send this in the `Authorization: Bearer <key>` header to access protected endpoints. Generate a strong random value:

```bash
openssl rand -hex 32
```

### `tls`

Enable HTTPS. Provide paths to your certificate and private key files. Behind a reverse proxy (nginx, Caddy, Cloudflare), TLS termination is typically handled externally — leave `tls.enabled: false`.

## Example: local dev

```yaml
server:
  host: 127.0.0.1
  port: 18789
  api_key: "dev-only-key"
```

## Example: production with TLS

```yaml
server:
  host: 0.0.0.0
  port: 443
  api_key: "sy_<strong-random-value>"
  tls:
    enabled: true
    cert_file: /etc/letsencrypt/live/example.com/fullchain.pem
    key_file:  /etc/letsencrypt/live/example.com/privkey.pem
```
