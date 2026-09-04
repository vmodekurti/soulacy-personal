"""
sy-py — Soulacy Python CLI.

Provides a command-line interface for interacting with the Soulacy gateway,
running local Python agents, and managing the framework.

Usage::

    sy-py chat --agent my-agent "Hello!"
    sy-py chat --agent my-agent --stream "Tell me a story"
    sy-py agents
    sy-py agents get my-agent
    sy-py agents enable my-agent
    sy-py agents disable my-agent
    sy-py agents trigger my-agent
    sy-py memory --agent my-agent
    sy-py health
    sy-py run path/to/agent.py          # serve a Python-defined agent
    sy-py install path/to/agent.py      # register agent with the gateway

Environment variables:
    SOULACY_GATEWAY   Gateway URL (default: http://localhost:18789)
    SOULACY_API_KEY   API key (default: empty = dev mode)
"""

from __future__ import annotations

import argparse
import importlib.util
import json
import os
import sys
import textwrap
from typing import Optional
from urllib import request as urllib_request, error as urllib_error


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

_GATEWAY_ENV = "SOULACY_GATEWAY"
_KEY_ENV = "SOULACY_API_KEY"
_DEFAULT_GW = "http://localhost:18789"


def _gateway_url() -> str:
    return os.environ.get(_GATEWAY_ENV, _DEFAULT_GW).rstrip("/")


def _api_key() -> str:
    return os.environ.get(_KEY_ENV, "")


def _headers() -> dict:
    h = {"Content-Type": "application/json"}
    if key := _api_key():
        h["Authorization"] = f"Bearer {key}"
    return h


def _do(method: str, path: str, body: Optional[dict] = None, timeout: int = 30) -> dict:
    """Perform a gateway REST call, return parsed JSON."""
    url = _gateway_url() + "/api/v1" + path
    data = json.dumps(body).encode() if body is not None else None
    req = urllib_request.Request(url, data=data, method=method)
    for k, v in _headers().items():
        req.add_header(k, v)
    try:
        with urllib_request.urlopen(req, timeout=timeout) as resp:
            content = resp.read().decode()
            return json.loads(content) if content else {}
    except urllib_error.HTTPError as e:
        text = e.read().decode()
        try:
            msg = json.loads(text).get("error", text)
        except Exception:
            msg = text
        _fatal(f"HTTP {e.code}: {msg}")
    except urllib_error.URLError as e:
        _fatal(
            f"Cannot reach Soulacy gateway at {_gateway_url()}.\n"
            f"Is it running?  Hint: start it with `soulacy`.\n"
            f"Reason: {e.reason}"
        )


def _get(path: str) -> dict:
    return _do("GET", path)


def _post(path: str, body: Optional[dict] = None) -> dict:
    return _do("POST", path, body)


def _fatal(msg: str) -> None:
    print(f"error: {msg}", file=sys.stderr)
    sys.exit(1)


def _print_json(obj: dict, pretty: bool = True) -> None:
    indent = 2 if pretty else None
    print(json.dumps(obj, indent=indent, ensure_ascii=False))


def _fmt_agents(agents: list) -> None:
    if not agents:
        print("(no agents)")
        return
    for a in agents:
        status = "✓" if a.get("enabled") else "✗"
        provider = (a.get("llm") or {}).get("provider", "?")
        model = (a.get("llm") or {}).get("model", "?")
        print(f"  {status}  {a['id']:<30}  {provider}/{model}  trigger={a.get('trigger','?')}")


# ---------------------------------------------------------------------------
# Subcommand handlers
# ---------------------------------------------------------------------------

def cmd_chat(args: argparse.Namespace) -> None:
    """POST a message to an agent and print the reply."""
    text = " ".join(args.text) if isinstance(args.text, list) else args.text
    if not text:
        _fatal("no message text provided")
    payload = {
        "agent_id": args.agent,
        "user_id": args.user or "sy-py",
        "username": args.user or "sy-py",
        "text": text,
    }
    if args.stream:
        _chat_stream(payload)
    else:
        result = _post("/chat", payload)
        print(result.get("reply", ""))


def _chat_stream(payload: dict) -> None:
    """Stream tokens from /chat/stream using server-sent events."""
    url = _gateway_url() + "/api/v1/chat/stream"
    data = json.dumps(payload).encode()
    req = urllib_request.Request(url, data=data, method="POST")
    for k, v in _headers().items():
        req.add_header(k, v)
    req.add_header("Accept", "text/event-stream")

    try:
        with urllib_request.urlopen(req, timeout=300) as resp:
            for raw_line in resp:
                line = raw_line.decode().rstrip("\n\r")
                if not line.startswith("data: "):
                    continue
                chunk = line[len("data: "):]
                if chunk == "[DONE]":
                    print()  # final newline
                    break
                if chunk.startswith("event: error"):
                    continue
                # Unescape embedded newlines (the server escapes \n → \\n).
                chunk = chunk.replace("\\n", "\n")
                print(chunk, end="", flush=True)
    except urllib_error.HTTPError as e:
        _fatal(f"stream HTTP {e.code}: {e.read().decode()}")
    except urllib_error.URLError as e:
        _fatal(f"stream connection error: {e.reason}")


def cmd_agents_list(args: argparse.Namespace) -> None:
    data = _get("/agents")
    agents = data.get("agents", [])
    if args.json:
        _print_json(data)
        return
    print(f"Agents ({len(agents)}):")
    _fmt_agents(agents)


def cmd_agents_get(args: argparse.Namespace) -> None:
    data = _get(f"/agents/{args.id}")
    _print_json(data)


def cmd_agents_enable(args: argparse.Namespace) -> None:
    data = _post(f"/agents/{args.id}/enable")
    print(f"{'Enabled' if data.get('enabled') else 'Disabled'}: {args.id}")


def cmd_agents_disable(args: argparse.Namespace) -> None:
    data = _post(f"/agents/{args.id}/disable")
    print(f"{'Enabled' if data.get('enabled') else 'Disabled'}: {args.id}")


def cmd_agents_trigger(args: argparse.Namespace) -> None:
    data = _post(f"/agents/{args.id}/trigger")
    print(data.get("result", "(no reply)"))


def cmd_memory(args: argparse.Namespace) -> None:
    q = getattr(args, "query", None)
    path = f"/memory/{args.agent}"
    if q:
        path += f"?q={urllib_request.quote(q)}"
    data = _get(path)
    if args.json:
        _print_json(data)
        return
    entries = data.get("entries") or []
    if not entries:
        print("(no memory entries)")
        return
    for e in entries:
        ts = e.get("created_at", "")[:19]
        scope = e.get("scope", "?")
        content = e.get("content", "")
        short = textwrap.shorten(content, width=120, placeholder="…")
        print(f"  [{ts}] [{scope}] {short}")


def cmd_health(args: argparse.Namespace) -> None:
    data = _get("/health")
    if args.json:
        _print_json(data)
        return
    status = data.get("status", "?")
    version = data.get("version", "?")
    deps = data.get("deps", {})
    marker = "✓" if status == "ok" else ("⚠" if status == "degraded" else "✗")
    print(f"{marker} Soulacy {version}  ({status})")
    for k, v in sorted(deps.items()):
        icon = "✓" if v == "ok" else "·"
        print(f"  {icon} {k}: {v}")


def cmd_run(args: argparse.Namespace) -> None:
    """Load a Python agent file and call .serve() on the decorated AgentRunner."""
    path = args.file
    if not os.path.isfile(path):
        _fatal(f"file not found: {path}")

    spec = importlib.util.spec_from_file_location("_sy_agent", path)
    if spec is None or spec.loader is None:
        _fatal(f"cannot load: {path}")
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)  # type: ignore[union-attr]

    # Find an AgentRunner in the module's top-level names.
    from .agent import AgentRunner
    runners = [v for v in vars(mod).values() if isinstance(v, AgentRunner)]
    if not runners:
        _fatal(f"no @agent-decorated function found in {path}")
    if len(runners) > 1:
        print(f"warning: multiple agents found, serving the first: {runners[0].id}")

    runner = runners[0]
    runner.serve(
        host=args.host,
        port=args.port,
        auto_register=not args.no_register,
        gateway_url=_gateway_url() if not args.no_register else None,
        api_key=_api_key() if not args.no_register else None,
    )


def cmd_install(args: argparse.Namespace) -> None:
    """Register an agent file's definition with the gateway (no serve)."""
    path = args.file
    if not os.path.isfile(path):
        _fatal(f"file not found: {path}")

    spec = importlib.util.spec_from_file_location("_sy_agent", path)
    mod = importlib.util.module_from_spec(spec)  # type: ignore[arg-type]
    spec.loader.exec_module(mod)  # type: ignore[union-attr]

    from .agent import AgentRunner
    runners = [v for v in vars(mod).values() if isinstance(v, AgentRunner)]
    if not runners:
        _fatal(f"no @agent-decorated function found in {path}")

    from .client import SoulacyClient
    client = SoulacyClient(gateway_url=_gateway_url(), api_key=_api_key())
    for runner in runners:
        try:
            client.create_agent(runner.definition())
            print(f"✓ registered: {runner.id}")
        except Exception as exc:
            print(f"✗ {runner.id}: {exc}", file=sys.stderr)


# ---------------------------------------------------------------------------
# Argument parser
# ---------------------------------------------------------------------------

def _build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="sy-py",
        description="Soulacy Python CLI — interact with the Soulacy gateway.",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog=textwrap.dedent("""
            Environment:
              SOULACY_GATEWAY   Gateway URL (default: http://localhost:18789)
              SOULACY_API_KEY   API key

            Examples:
              sy-py chat --agent my-agent "Hello!"
              sy-py chat --agent my-agent --stream "Tell me a story"
              sy-py agents
              sy-py agents get my-agent
              sy-py health
              sy-py run my_agent.py
        """),
    )
    parser.add_argument(
        "--gateway", metavar="URL",
        help="Gateway URL (overrides $SOULACY_GATEWAY)",
    )
    parser.add_argument(
        "--key", metavar="KEY",
        help="API key (overrides $SOULACY_API_KEY)",
    )

    sub = parser.add_subparsers(dest="command", metavar="command")

    # ── chat ────────────────────────────────────────────────────────────────
    p_chat = sub.add_parser("chat", help="Send a message to an agent and print the reply.")
    p_chat.add_argument("--agent", "-a", required=True, help="Agent ID")
    p_chat.add_argument("--user", "-u", default="sy-py", help="User ID / username")
    p_chat.add_argument("--stream", "-s", action="store_true",
                        help="Stream tokens via SSE as they arrive")
    p_chat.add_argument("text", nargs="+", help="Message text")

    # ── agents ──────────────────────────────────────────────────────────────
    p_agents = sub.add_parser("agents", help="Manage agents.")
    agents_sub = p_agents.add_subparsers(dest="agents_cmd", metavar="action")
    agents_sub.add_parser("list", help="List all agents (default).")
    # If no action given, list is the default — handled in cmd_agents dispatch.

    p_agents_get = agents_sub.add_parser("get", help="Show full definition of one agent.")
    p_agents_get.add_argument("id", help="Agent ID")

    p_agents_en = agents_sub.add_parser("enable", help="Enable an agent.")
    p_agents_en.add_argument("id", help="Agent ID")

    p_agents_dis = agents_sub.add_parser("disable", help="Disable an agent.")
    p_agents_dis.add_argument("id", help="Agent ID")

    p_agents_trig = agents_sub.add_parser("trigger", help="Manually trigger an agent.")
    p_agents_trig.add_argument("id", help="Agent ID")

    # --json flag on agents list
    p_agents.add_argument("--json", action="store_true", help="Output raw JSON")

    # ── memory ──────────────────────────────────────────────────────────────
    p_mem = sub.add_parser("memory", help="List memory entries for an agent.")
    p_mem.add_argument("--agent", "-a", required=True, help="Agent ID")
    p_mem.add_argument("--query", "-q", default="", help="Search query (substring match)")
    p_mem.add_argument("--json", action="store_true", help="Output raw JSON")

    # ── health ──────────────────────────────────────────────────────────────
    p_health = sub.add_parser("health", help="Check gateway health.")
    p_health.add_argument("--json", action="store_true", help="Output raw JSON")

    # ── run ─────────────────────────────────────────────────────────────────
    p_run = sub.add_parser("run", help="Serve a Python agent file (register + tool server).")
    p_run.add_argument("file", help="Path to the Python agent file")
    p_run.add_argument("--host", default="127.0.0.1", help="Bind host (default: 127.0.0.1)")
    p_run.add_argument("--port", type=int, default=None,
                       help="Bind port (default: $SOULACY_AGENT_PORT or 19000)")
    p_run.add_argument("--no-register", action="store_true",
                       help="Do not push agent definition to gateway on startup")

    # ── install ─────────────────────────────────────────────────────────────
    p_install = sub.add_parser("install",
                               help="Register a Python agent with the gateway (no tool server).")
    p_install.add_argument("file", help="Path to the Python agent file")

    return parser


# ---------------------------------------------------------------------------
# Entry point
# ---------------------------------------------------------------------------

def main(argv: Optional[list] = None) -> None:  # noqa: C901
    parser = _build_parser()
    args = parser.parse_args(argv)

    # Apply gateway/key overrides before any _do() call.
    if args.gateway:
        os.environ[_GATEWAY_ENV] = args.gateway
    if args.key:
        os.environ[_KEY_ENV] = args.key

    cmd = args.command
    if cmd is None:
        parser.print_help()
        return

    if cmd == "chat":
        cmd_chat(args)

    elif cmd == "agents":
        action = getattr(args, "agents_cmd", None)
        if action is None or action == "list":
            cmd_agents_list(args)
        elif action == "get":
            cmd_agents_get(args)
        elif action == "enable":
            cmd_agents_enable(args)
        elif action == "disable":
            cmd_agents_disable(args)
        elif action == "trigger":
            cmd_agents_trigger(args)
        else:
            parser.parse_args(["agents", "--help"])

    elif cmd == "memory":
        cmd_memory(args)

    elif cmd == "health":
        cmd_health(args)

    elif cmd == "run":
        cmd_run(args)

    elif cmd == "install":
        cmd_install(args)

    else:
        parser.print_help()


if __name__ == "__main__":
    main()
