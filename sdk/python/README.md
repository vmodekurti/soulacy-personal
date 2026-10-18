# Soulacy Python SDK

> ⚠️ **Experimental.** This SDK is not yet published to PyPI and has
> request-contract tests in CI. APIs may change without notice. Install from
> source and pin to a commit if you depend on it.

Python SDK for the [Soulacy](https://github.com/vmodekurti/soulacy-personal) agentic
framework — define and run agents in Python.

## Install (from source)

```bash
pip install ./sdk/python
```

## Long runs and conversation status

Requests allow 48 hours plus one minute by default. Pass `timeout=None` or
`timeout=0` to wait without an SDK timeout, or a positive number of seconds for
an explicit deadline. Gateway and provider limits still apply.

`client.chat(agent_id, text, session_id=...)` returns the reply string.
Use `client.chat_response(...)` to retain the session ID, run ID, rich parts and
completion metadata, including incomplete results.

## License

Apache-2.0 — see the repository root `LICENSE`.
