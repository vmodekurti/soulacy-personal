# Channel Sidecar Starter

This is the smallest production-shaped External Channel Protocol sidecar.
Clone this folder when adding a new channel such as Matrix, Signal, SMS, or
Google Chat.

## Files

- `echo_sidecar.py`: dependency-free Python sidecar that passes the protocol
  conformance kit.
- `echo_sidecar_test.go`: Go test that runs the official conformance suite
  against the sidecar.

## Run The Contract Test

```bash
go test ./examples/channel-sidecar-starter
```

## Turn It Into A Real Channel

1. Replace `handle_send()` with the platform API call for outbound messages.
2. Add the platform event loop after `hello_ack` and emit `message` frames for
   inbound platform messages.
3. Keep `stderr` for diagnostics only. Never print tokens or private message
   contents there unless the user explicitly enables debug logging.
4. Run the conformance test in CI before shipping the plugin.

Plugin declaration sketch:

```yaml
id: my-channel
name: My Channel
version: 1.0.0
manifest_schema: 2

channels:
  - id: my_channel
    agent_id: assistant
    sidecar:
      command: python3
      args: ["sidecar/echo_sidecar.py"]

permissions:
  - cap: channel.send
    channels: [my_channel]

credentials:
  - key: MY_CHANNEL_TOKEN
    from: my-channel/token
```

Full protocol: `docs/EXTERNAL_CHANNEL_PROTOCOL.md`.
