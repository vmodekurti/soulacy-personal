#!/usr/bin/env python3
"""
Rocket Money MCP Server
GraphQL endpoint: https://client-api.rocketmoney.com/graphql
Auth: browser session cookies (auto-extracted from Chrome) or ROCKET_MONEY_COOKIE env var.
"""

import os
import sys
import subprocess

# Auto-install missing dependencies on first run
_DEPS = ["mcp>=1.0.0", "httpx>=0.27.0", "browser-cookie3>=0.19.1"]

def _pip_install():
    """Try several pip invocation styles until one works."""
    attempts = [
        [sys.executable, "-m", "pip", "install", "--quiet"] + _DEPS,
        [sys.executable, "-m", "pip", "install", "--quiet", "--break-system-packages"] + _DEPS,
        [sys.executable, "-m", "pip", "install", "--quiet", "--user"] + _DEPS,
    ]
    for cmd in attempts:
        result = subprocess.run(cmd, capture_output=True)
        if result.returncode == 0:
            return
    # Last attempt — loud, so Soulacy logs capture the error
    subprocess.run([sys.executable, "-m", "pip", "install"] + _DEPS, check=True)

def _ensure_deps():
    missing = []
    for mod in ("mcp", "httpx", "browser_cookie3"):
        try:
            __import__(mod)
        except ImportError:
            missing.append(mod)
    if missing:
        print(f"[rocketmoney-mcp] Installing: {missing}", file=sys.stderr)
        _pip_install()
        print("[rocketmoney-mcp] Done.", file=sys.stderr)

_ensure_deps()

import json
import logging
import httpx
from mcp.server import Server
from mcp.server.stdio import stdio_server
from mcp import types

logging.basicConfig(level=logging.WARNING, stream=sys.stderr)
log = logging.getLogger("rocketmoney-mcp")

GQL_URL = "https://client-api.rocketmoney.com/graphql"

# ---------------------------------------------------------------------------
# Auth cookie resolution
# ---------------------------------------------------------------------------

def _cookie_from_browser() -> str:
    """Try to read Rocket Money cookies directly from the Chrome cookie store."""
    try:
        import browser_cookie3
        jar = browser_cookie3.chrome(domain_name=".rocketmoney.com")
        cookies = {c.name: c.value for c in jar}
        if not cookies:
            jar = browser_cookie3.chrome(domain_name="rocketmoney.com")
            cookies = {c.name: c.value for c in jar}
        if cookies:
            return "; ".join(f"{k}={v}" for k, v in cookies.items())
    except Exception as e:
        log.warning("browser_cookie3 failed: %s", e)
    return ""


def get_cookie() -> str:
    # 1. Explicit env var takes priority
    cookie = os.environ.get("ROCKET_MONEY_COOKIE", "").strip()
    if cookie:
        return cookie
    # 2. Auto-extract from Chrome
    cookie = _cookie_from_browser()
    if cookie:
        log.info("Using cookies extracted from Chrome")
        return cookie
    raise RuntimeError(
        "No Rocket Money auth cookie found. "
        "Set ROCKET_MONEY_COOKIE env var or ensure you are logged in to "
        "app.rocketmoney.com in Chrome."
    )


# ---------------------------------------------------------------------------
# GraphQL client
# ---------------------------------------------------------------------------

def gql(query: str, variables: dict | None = None) -> dict:
    cookie = get_cookie()
    headers = {
        "Content-Type": "application/json",
        "Accept": "application/json",
        "Cookie": cookie,
        "Origin": "https://app.rocketmoney.com",
        "Referer": "https://app.rocketmoney.com/",
        "User-Agent": (
            "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) "
            "AppleWebKit/537.36 (KHTML, like Gecko) "
            "Chrome/124.0.0.0 Safari/537.36"
        ),
    }
    payload = {"query": query}
    if variables:
        payload["variables"] = variables

    with httpx.Client(timeout=30) as client:
        resp = client.post(GQL_URL, json=payload, headers=headers)
        resp.raise_for_status()
        data = resp.json()

    if "errors" in data and not data.get("data"):
        errs = "; ".join(e["message"] for e in data["errors"])
        raise RuntimeError(f"GraphQL errors: {errs}")
    return data.get("data", {})


# ---------------------------------------------------------------------------
# MCP Server
# ---------------------------------------------------------------------------

server = Server("rocketmoney")


@server.list_tools()
async def list_tools() -> list[types.Tool]:
    return [
        types.Tool(
            name="get_transactions",
            description=(
                "List Rocket Money transactions. Returns id, amount (cents), date, "
                "merchantShortName, pending, note, and category id. "
                "Supports cursor-based pagination."
            ),
            inputSchema={
                "type": "object",
                "properties": {
                    "limit": {
                        "type": "integer",
                        "description": "Number of transactions to return (default 25, max 100)",
                        "default": 25,
                    },
                    "after": {
                        "type": "string",
                        "description": "Pagination cursor from previous response endCursor",
                    },
                    "start_date": {
                        "type": "string",
                        "description": "Filter: start date YYYY-MM-DD (inclusive)",
                    },
                    "end_date": {
                        "type": "string",
                        "description": "Filter: end date YYYY-MM-DD (inclusive)",
                    },
                },
            },
        ),
        types.Tool(
            name="get_accounts",
            description=(
                "List all linked financial accounts with their current balances. "
                "Amounts are in cents."
            ),
            inputSchema={"type": "object", "properties": {}},
        ),
        types.Tool(
            name="get_net_worth",
            description=(
                "Return net worth breakdown: assets (savings, investments, etc.) "
                "and debts (loans, credit cards, mortgage). "
                "Use get_accounts for precise balances."
            ),
            inputSchema={"type": "object", "properties": {}},
        ),
        types.Tool(
            name="get_budget",
            description=(
                "Return the current budget plan with individual budget items. "
                "Amounts are in cents."
            ),
            inputSchema={"type": "object", "properties": {}},
        ),
        types.Tool(
            name="get_subscriptions",
            description=(
                "List tracked subscriptions and recurring services in Rocket Money."
            ),
            inputSchema={"type": "object", "properties": {}},
        ),
        types.Tool(
            name="get_user",
            description="Return the authenticated Rocket Money user's profile.",
            inputSchema={"type": "object", "properties": {}},
        ),
    ]


@server.call_tool()
async def call_tool(name: str, arguments: dict) -> list[types.TextContent]:
    try:
        result = _dispatch(name, arguments)
        return [types.TextContent(type="text", text=json.dumps(result, indent=2))]
    except Exception as exc:
        return [types.TextContent(type="text", text=f"Error: {exc}")]


def _dispatch(name: str, args: dict) -> dict:
    if name == "get_transactions":
        return _get_transactions(
            limit=min(int(args.get("limit", 25)), 100),
            after=args.get("after"),
            start_date=args.get("start_date"),
            end_date=args.get("end_date"),
        )
    if name == "get_accounts":
        return _get_accounts()
    if name == "get_net_worth":
        return _get_net_worth()
    if name == "get_budget":
        return _get_budget()
    if name == "get_subscriptions":
        return _get_subscriptions()
    if name == "get_user":
        return _get_user()
    raise ValueError(f"Unknown tool: {name}")


# ---------------------------------------------------------------------------
# Tool implementations
# ---------------------------------------------------------------------------

def _get_transactions(
    limit: int = 25,
    after: str | None = None,
    start_date: str | None = None,
    end_date: str | None = None,
) -> dict:
    # Build optional filter arguments
    filter_args = []
    if start_date:
        filter_args.append(f'startDate: "{start_date}"')
    if end_date:
        filter_args.append(f'endDate: "{end_date}"')
    filter_str = (", " + ", ".join(filter_args)) if filter_args else ""

    after_str = f', after: "{after}"' if after else ""

    query = f"""
    {{
      viewer {{
        transactions(first: {limit}{after_str}{filter_str}) {{
          edges {{
            node {{
              id
              amount
              date
              merchantShortName
              pending
              note
              category {{ id }}
            }}
          }}
          pageInfo {{
            hasNextPage
            endCursor
          }}
        }}
      }}
    }}
    """
    data = gql(query)
    txns = data.get("viewer", {}).get("transactions", {})
    edges = txns.get("edges", [])
    transactions = [e["node"] for e in edges if e.get("node")]
    # Convert amounts from cents to dollars for readability
    for t in transactions:
        t["amount_dollars"] = t["amount"] / 100.0
    return {
        "transactions": transactions,
        "page_info": txns.get("pageInfo", {}),
        "count": len(transactions),
        "note": "amount is in cents; amount_dollars is the dollar value",
    }


def _get_accounts() -> dict:
    query = """
    {
      viewer {
        accounts(first: 50) {
          edges {
            node {
              id
              name
              currentBalance
              displayedBalance
              institution { id name }
            }
          }
        }
      }
    }
    """
    data = gql(query)
    edges = data.get("viewer", {}).get("accounts", {}).get("edges", [])
    accounts = [e["node"] for e in edges if e.get("node")]
    for a in accounts:
        a["currentBalance_dollars"] = a.get("currentBalance", 0) / 100.0
        a["displayedBalance_dollars"] = a.get("displayedBalance", 0) / 100.0
    return {
        "accounts": accounts,
        "count": len(accounts),
        "note": "balances are in cents; _dollars fields are dollar values",
    }


def _get_net_worth() -> dict:
    query = """
    {
      viewer {
        netWorth {
          assets { id name }
          debts  { id name }
        }
      }
    }
    """
    data = gql(query)
    nw = data.get("viewer", {}).get("netWorth", {})
    return {
        "assets": nw.get("assets", []),
        "debts": nw.get("debts", []),
        "asset_count": len(nw.get("assets", [])),
        "debt_count": len(nw.get("debts", [])),
        "tip": "Use get_accounts to see precise balances for each account",
    }


def _get_budget() -> dict:
    query = """
    {
      viewer {
        budgetPlan {
          id
          budgetItems {
            id
            amount
          }
        }
      }
    }
    """
    data = gql(query)
    plan = data.get("viewer", {}).get("budgetPlan") or {}
    items = plan.get("budgetItems", [])
    for item in items:
        item["amount_dollars"] = item.get("amount", 0) / 100.0
    return {
        "budget_plan_id": plan.get("id"),
        "budget_items": items,
        "item_count": len(items),
        "note": "amount is in cents; amount_dollars is the dollar value",
    }


def _get_subscriptions() -> dict:
    query = """
    {
      viewer {
        allServices {
          edges {
            node {
              id
              name
            }
          }
        }
      }
    }
    """
    data = gql(query)
    edges = data.get("viewer", {}).get("allServices", {}).get("edges", [])
    services = [e["node"] for e in edges if e.get("node")]
    return {
        "subscriptions": services,
        "count": len(services),
    }


def _get_user() -> dict:
    query = """
    {
      viewer {
        id
        email
        name
      }
    }
    """
    data = gql(query)
    return data.get("viewer", {})


# ---------------------------------------------------------------------------
# Entry point
# ---------------------------------------------------------------------------

async def main():
    async with stdio_server() as (read_stream, write_stream):
        await server.run(read_stream, write_stream, server.create_initialization_options())


if __name__ == "__main__":
    import asyncio
    asyncio.run(main())
