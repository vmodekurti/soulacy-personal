"""
Tool decorator — wraps a Python function as a Soulacy tool.

A tool is a callable that the LLM can invoke during an agentic loop.
Soulacy executes tools in a sandboxed subprocess (via the engine) and
returns the result as a string back to the LLM.

Usage::

    from soulacy import tool

    @tool
    def search_web(query: str) -> str:
        \"\"\"Search the web for information.\"\"\"
        # your implementation here
        return f"Results for: {query}"

    # Tools can also declare their JSON Schema parameters explicitly:
    @tool(
        description="Fetch the content of a URL",
        parameters={
            "type": "object",
            "properties": {
                "url": {"type": "string", "description": "The URL to fetch"},
            },
            "required": ["url"],
        }
    )
    def fetch_url(url: str) -> str:
        import urllib.request
        with urllib.request.urlopen(url) as r:
            return r.read(4096).decode()
"""

from __future__ import annotations

import inspect
import json
from typing import Any, Callable, Optional


class Tool:
    """A registered Soulacy tool."""

    def __init__(
        self,
        fn: Callable,
        description: Optional[str] = None,
        parameters: Optional[dict] = None,
    ) -> None:
        self.fn = fn
        self.name = fn.__name__
        self.description = description or (inspect.getdoc(fn) or self.name)
        self.parameters = parameters or _infer_parameters(fn)

    def __call__(self, **kwargs: Any) -> str:
        result = self.fn(**kwargs)
        if isinstance(result, str):
            return result
        return json.dumps(result)

    def schema(self) -> dict:
        """Return the JSON Schema descriptor for this tool."""
        return {
            "name": self.name,
            "description": self.description,
            "parameters": self.parameters,
        }

    def run_from_stdin(self) -> None:
        """Execute the tool by reading JSON kwargs from stdin and printing the result.

        This is the entrypoint used by the Go engine when it spawns a Python subprocess
        to run a tool. The engine writes JSON-encoded arguments to stdin and reads the
        result from stdout.
        """
        import sys
        raw = sys.stdin.read()
        kwargs = json.loads(raw) if raw.strip() else {}
        print(self(**kwargs))


def tool(fn: Optional[Callable] = None, *, description: str = None, parameters: dict = None):
    """Decorator that turns a Python function into a Soulacy Tool.

    Can be used with or without arguments:

        @tool
        def my_tool(x: str) -> str: ...

        @tool(description="Custom description")
        def my_tool(x: str) -> str: ...
    """
    if fn is not None:
        # Called as @tool (no parens)
        return Tool(fn)

    # Called as @tool(...) — return a decorator
    def decorator(f: Callable) -> Tool:
        return Tool(f, description=description, parameters=parameters)

    return decorator


def _infer_parameters(fn: Callable) -> dict:
    """Infer a JSON Schema from Python type annotations."""
    sig = inspect.signature(fn)
    hints = {}
    try:
        hints = fn.__annotations__
    except AttributeError:
        pass

    _type_map = {
        "str": "string",
        "int": "integer",
        "float": "number",
        "bool": "boolean",
        "list": "array",
        "dict": "object",
    }

    properties = {}
    required = []

    for name, param in sig.parameters.items():
        annotation = hints.get(name, inspect.Parameter.empty)
        json_type = "string"  # default
        if annotation is not inspect.Parameter.empty:
            type_name = getattr(annotation, "__name__", str(annotation))
            json_type = _type_map.get(type_name, "string")

        properties[name] = {"type": json_type, "description": name}

        if param.default is inspect.Parameter.empty:
            required.append(name)

    return {
        "type": "object",
        "properties": properties,
        "required": required,
    }
