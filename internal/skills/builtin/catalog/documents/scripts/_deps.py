"""Shared helper: import a dependency or exit 3 with a hint the agent can act on."""
import importlib
import sys


def need(module: str, package: str):
    try:
        return importlib.import_module(module)
    except ImportError:
        print(f"MISSING: {package}  (install with install_library, then rerun)")
        sys.exit(3)
