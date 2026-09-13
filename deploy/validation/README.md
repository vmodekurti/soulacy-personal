# Isolated Autopilot validation

`run.py` executes ten compiled Go test suites and starts the actual gateway with
a temporary workspace and a loopback-only fixture LLM. It exercises the HTTP,
SQLite, release, goal, device-claim, idempotency, published-file, Safe Undo,
private learning and restart boundaries. Learning checks cover teaching,
citations, approval, duplicate requests, feedback, disabling and restoration.
Two loopback HTTP fixtures implement conditional JSON Patch and WebDAV text writes, including lost acknowledgements. These are protocol fixtures, not vendor SaaS integrations. It never
uses production credentials or represents its fixture phone as a physical test.

From the repository root, when registries are reachable:

```sh
docker build --platform linux/amd64 -f deploy/validation/Dockerfile -t soulacy-autopilot-validation .
docker run --rm --network none --cpus 1 --memory 1g --pids-limit 256 \
  --read-only --tmpfs /tmp:rw,nosuid,size=512m \
  --cap-drop ALL --security-opt no-new-privileges \
  soulacy-autopilot-validation
```

The Dockerfile-specific allowlist excludes deployment configuration, environment
files, source-control metadata, node_modules, and production data. Do not replace
it with a broad context or mount the Docker socket, production data, home
directory, or cloud credentials into the validation container.

## Offline Linux builder

`build-binaries.sh` is a fallback for unavailable registries. Run it inside a
Linux/amd64 container containing GCC, with Go 1.26.6 and required SQLite public
headers available. Mount only `go.mod`, `go.sum`, `sdk`, `internal`, `pkg`, and
`cmd` read-only at `/src`, the Go module cache read-only, this directory at
`/validation`, and a fresh writable temporary directory at `/out`. Set
`GOCACHE=/out/cache`, `CGO_ENABLED=1`, and the Go toolchain path. Build the web
assets first with `npm ci` and `npm run build` in `gui`.

The script emits the gateway, ten test executables, and the Python runner.
Transfer only those twelve files, not the build cache or source tree. Verify the
archive checksum on the destination. A short-lived private object download is
sufficient; there is no need to alter host IAM roles or expose a test port.

When packaging on macOS, omit extended attributes and AppleDouble metadata.
Use an explicit file list so a cache, hidden file or credential cannot enter the
archive accidentally:

```sh
COPYFILE_DISABLE=1 tar --no-xattrs -czf /tmp/soulacy-validation.tar.gz \
  -C /path/to/binaries soulacy autopilot.test gateway.test runtime.test \
  mobile.test llm.test costs.test discovery.test publishedfiles.test \
  safeundo.test learning.test run.py
```

Verify that the archive has exactly those twelve regular files, then calculate
its SHA-256. The destination must reject unexpected entries, links and a
mismatched checksum before extracting or starting the container.

To run the bundle using an existing compatible Linux runtime image, mount it
read-only at `/validation`, retain the isolation flags above, and override the
entrypoint with `python3 /validation/run.py`. Match CPU architecture and ensure
the runtime has Python 3.11+ and compatible libc.

Record the host/container baseline before running; compare production IDs,
start times, health, and running states afterward. Remove only the temporary
resources created for the test. Never apply the fixture configuration to a live
workspace.

See [the validation report](../../docs/AUTOPILOT_VALIDATION_2026-09-12.md) for
actual results, artifact identity, cleanup, and remaining release gates.
