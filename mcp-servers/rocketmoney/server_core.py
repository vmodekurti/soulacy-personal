#!/usr/bin/env python3
"""Rocket Money MCP Server — core (deps must be pre-installed)."""
import os, sys, json, logging, subprocess
import httpx
from mcp.server import Server
from mcp.server.stdio import stdio_server
from mcp import types

logging.basicConfig(level=logging.WARNING, stream=sys.stderr)
log = logging.getLogger("rocketmoney-mcp")
GQL_URL = "https://client-api.rocketmoney.com/graphql"

_SECRETS_FILE = os.path.expanduser("~/.soulacy/secrets/rocket_money_cookie")

def get_cookie():
    """Return the Rocket Money session cookie.

    Resolution order:
    1. ~/.soulacy/secrets/rocket_money_cookie  — written by refresh_cookie()
       or the manual refresh script; always wins when present so an explicit
       refresh is never immediately overwritten by stale Chrome cookies.
    2. ROCKET_MONEY_COOKIE env var              — legacy fallback only.
    """
    # 1. Secrets file — authoritative when present (written by rm_session_manager)
    if os.path.exists(_SECRETS_FILE):
        val = open(_SECRETS_FILE).read().strip()
        if val:
            return val

    # 2. Env var (legacy fallback)
    c = os.environ.get("ROCKET_MONEY_COOKIE", "").strip()
    if c:
        return c

    raise RuntimeError(
        "AUTH: Rocket Money session not found. "
        "Run: python3.11 ~/.soulacy/mcp-servers/rocketmoney/rm_session_manager.py --setup"
    )

def _try_playwright_refresh() -> bool:
    """Call rm_session_manager --refresh. Returns True if it succeeded."""
    script = os.path.join(os.path.dirname(__file__), "rm_session_manager.py")
    if not os.path.exists(script):
        return False
    try:
        result = subprocess.run(
            ["python3.11", script, "--refresh"],
            capture_output=True, text=True, timeout=60,
        )
        return result.returncode == 0
    except Exception:
        return False


def gql(query, variables=None, _retry=True):
    try:
        data = _gql_once(query, variables)
    except RuntimeError as e:
        if str(e) == "AUTH_HTTP_401":
            if _retry:
                return _refresh_and_retry(query, variables)
            raise RuntimeError(
                "AUTH: Rocket Money session is still unauthorized after refresh. "
                "Run setup: python3.11 ~/.soulacy/mcp-servers/rocketmoney/rm_session_manager.py --setup"
            )
        raise

    # Auto-refresh on GraphQL auth errors and retry once.
    errors = data.get("errors", [])
    is_auth_error = any(
        "Authentication" in e.get("message", "") or
        e.get("code", "") == "GRAPHQL_REQUIRES_AUTHENTICATION"
        for e in errors
    )
    if is_auth_error and _retry:
        return _refresh_and_retry(query, variables)

    if errors and not data.get("data"):
        raise RuntimeError("; ".join(e["message"] for e in errors))
    return data.get("data", {})

def _gql_once(query, variables=None):
    headers = {
        "Content-Type": "application/json", "Accept": "application/json",
        "Cookie": get_cookie(), "Origin": "https://app.rocketmoney.com",
        "Referer": "https://app.rocketmoney.com/",
        "User-Agent": "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36"
    }
    with httpx.Client(timeout=30) as client:
        r = client.post(GQL_URL, json={"query": query, **({"variables": variables} if variables else {})}, headers=headers)
        if r.status_code == 401:
            raise RuntimeError("AUTH_HTTP_401")
        r.raise_for_status()
        return r.json()

def _refresh_and_retry(query, variables=None):
    log.warning("Rocket Money auth failed — attempting automatic session refresh...")
    if _try_playwright_refresh():
        log.warning("Session refreshed — retrying request.")
        # get_cookie() reads the secrets file each call, so the retry gets the
        # fresh cookie written by rm_session_manager.py.
        return gql(query, variables, _retry=False)
    raise RuntimeError(
        "AUTH: Rocket Money session expired and auto-refresh failed. "
        "Run setup: python3.11 ~/.soulacy/mcp-servers/rocketmoney/rm_session_manager.py --setup"
    )

server = Server("rocketmoney")

@server.list_tools()
async def list_tools():
    return [
        types.Tool(name="get_transactions", description="List Rocket Money transactions (amount in cents, date YYYY-MM-DD, supports pagination).",
            inputSchema={"type":"object","properties":{"limit":{"type":"integer","default":25},"after":{"type":"string"},"start_date":{"type":"string"},"end_date":{"type":"string"}}}),
        types.Tool(name="get_accounts", description="List linked financial accounts with balances (in cents).", inputSchema={"type":"object","properties":{}}),
        types.Tool(name="get_net_worth", description="Net worth: assets and debts by account name.", inputSchema={"type":"object","properties":{}}),
        types.Tool(name="get_budget", description="Current budget plan with items (amounts in cents).", inputSchema={"type":"object","properties":{}}),
        types.Tool(name="get_subscriptions", description="Tracked subscriptions and recurring services.", inputSchema={"type":"object","properties":{}}),
        types.Tool(name="get_user", description="Authenticated Rocket Money user profile.", inputSchema={"type":"object","properties":{}}),
        types.Tool(name="debug_net_worth", description="Raw net worth debug data — shows all account names, IDs, balances, and the netWorth asset/debt classification lists. Use to diagnose categorization issues.", inputSchema={"type":"object","properties":{}}),
    ]

@server.call_tool()
async def call_tool(name, arguments):
    try:
        r = _dispatch(name, arguments or {})
        return [types.TextContent(type="text", text=json.dumps(r, indent=2))]
    except Exception as e:
        return [types.TextContent(type="text", text=f"Error: {e}")]

def _dispatch(name, a):
    if name == "get_transactions":
        lim = min(int(a.get("limit", 25)), 100)
        af  = f', after: "{a["after"]}"' if a.get("after") else ""
        fs  = "".join([f', startDate: "{a["start_date"]}"' if a.get("start_date") else "", f', endDate: "{a["end_date"]}"' if a.get("end_date") else ""])
        d   = gql(f'{{ viewer {{ transactions(first: {lim}{af}{fs}) {{ edges {{ node {{ id amount date merchantShortName pending note category {{ id }} }} }} pageInfo {{ hasNextPage endCursor }} }} }} }}')
        txns = d.get("viewer",{}).get("transactions",{})
        nodes = [e["node"] for e in txns.get("edges",[]) if e.get("node")]
        for t in nodes: t["amount_dollars"] = t["amount"]/100.0
        return {"transactions": nodes, "page_info": txns.get("pageInfo",{}), "count": len(nodes)}
    if name == "get_accounts":
        # includeInNetWorth filters out accounts the user has hidden, closed, or
        # manually excluded from their net worth view — same filter Rocket Money's
        # own website applies. Accounts with the field absent default to included.
        d = gql('{ viewer { accounts(first: 200) { edges { node { id name currentBalance displayedBalance includeInNetWorth institution { id name } } } } } }')
        nodes = [
            e["node"] for e in d.get("viewer",{}).get("accounts",{}).get("edges",[])
            if e.get("node") and e.get("node",{}).get("includeInNetWorth", True)
        ]
        for n in nodes:
            n["currentBalance_dollars"] = n.get("currentBalance",0)/100.0
            n["displayedBalance_dollars"] = n.get("displayedBalance",0)/100.0
        return {"accounts": nodes, "count": len(nodes)}
    if name == "get_net_worth":
        # Strategy: use netWorth endpoint for asset/debt categorization (it knows
        # which accounts are which), join to accounts by NAME for balances.
        # We cannot join by ID — the two endpoints use different ID namespaces.
        # We cannot join by balance sign — Rocket Money returns credit card
        # balances as positive numbers, so sign-based categorization misfires.
        # Try to fetch includeInNetWorth — filters out accounts the user has
        # excluded from their net worth view (hidden, closed, manual exclusions).
        try:
            data = gql('''{ viewer {
                netWorth { assets { id name } debts { id name } }
                accounts(first: 200) { edges { node {
                    id name currentBalance displayedBalance
                    includeInNetWorth
                    institution { name }
                } } }
            } }''').get("viewer", {})
            all_edges = data.get("accounts", {}).get("edges", [])
            included = [e for e in all_edges if e.get("node", {}).get("includeInNetWorth", True)]
            data["accounts"]["edges"] = included
        except Exception:
            data = gql('''{ viewer {
                netWorth { assets { id name } debts { id name } }
                accounts(first: 200) { edges { node {
                    id name currentBalance displayedBalance institution { name }
                } } }
            } }''').get("viewer", {})

        nw = data.get("netWorth", {})
        # Build name → account lookup (lowercase for fuzzy match)
        acct_by_name = {}
        for e in data.get("accounts", {}).get("edges", []):
            n = e.get("node")
            if n:
                acct_by_name[n["name"].lower().strip()] = n

        # Build a set of debt account names from the netWorth endpoint.
        # Everything NOT in this set defaults to asset — avoids silently dropping
        # the many asset accounts whose names don't exactly match netWorth.assets.
        debt_names = {item["name"].lower().strip() for item in nw.get("debts", [])}

        def make_entry(acct):
            raw = acct.get("displayedBalance") if acct.get("displayedBalance") is not None else acct.get("currentBalance", 0)
            bal = abs(raw or 0)
            return {
                "name": acct.get("name", ""),
                "institution": (acct.get("institution") or {}).get("name", ""),
                "balance_cents": bal,
                "balance_dollars": bal / 100.0,
            }

        all_accounts = [e["node"] for e in data.get("accounts", {}).get("edges", []) if e.get("node")]
        assets = [make_entry(a) for a in all_accounts if a.get("name", "").lower().strip() not in debt_names]
        debts  = [make_entry(a) for a in all_accounts if a.get("name", "").lower().strip() in debt_names]

        total_assets = sum(a["balance_cents"] for a in assets)
        total_debts  = sum(d["balance_cents"] for d in debts)
        return {
            "assets": assets,
            "debts": debts,
            "total_assets_dollars": total_assets / 100.0,
            "total_debts_dollars":  total_debts  / 100.0,
            "net_worth_dollars":    (total_assets - total_debts) / 100.0,
            "note": "Debts identified by name from Rocket Money netWorth endpoint; all other accounts treated as assets.",
        }
    if name == "get_budget":
        plan = gql('{ viewer { budgetPlan { id budgetItems { id amount } } } }').get("viewer",{}).get("budgetPlan") or {}
        items = plan.get("budgetItems",[])
        for i in items: i["amount_dollars"] = i.get("amount",0)/100.0
        return {"budget_plan_id": plan.get("id"), "budget_items": items, "item_count": len(items)}
    if name == "get_subscriptions":
        nodes = [e["node"] for e in gql('{ viewer { allServices { edges { node { id name } } } } }').get("viewer",{}).get("allServices",{}).get("edges",[]) if e.get("node")]
        return {"subscriptions": nodes, "count": len(nodes)}
    if name == "debug_net_worth":
        try:
            data = gql('''{ viewer {
                netWorth { assets { id name } debts { id name } }
                accounts(first: 200) { edges { node {
                    id name currentBalance displayedBalance
                    includeInNetWorth institution { name }
                } } }
            } }''').get("viewer", {})
        except Exception:
            data = gql('''{ viewer {
                netWorth { assets { id name } debts { id name } }
                accounts(first: 200) { edges { node {
                    id name currentBalance displayedBalance institution { name }
                } } }
            } }''').get("viewer", {})
        nw = data.get("netWorth", {})
        accounts = [e["node"] for e in data.get("accounts", {}).get("edges", []) if e.get("node")]
        return {
            "networth_assets": nw.get("assets", []),
            "networth_debts": nw.get("debts", []),
            "all_accounts": [
                {
                    "name": a["name"],
                    "id": a["id"],
                    "institution": (a.get("institution") or {}).get("name", ""),
                    "currentBalance_dollars": (a.get("currentBalance") or 0) / 100.0,
                    "displayedBalance_dollars": (a.get("displayedBalance") or 0) / 100.0,
                }
                for a in accounts
            ],
            "account_count": len(accounts),
        }
    if name == "get_user":
        return gql('{ viewer { id email name } }').get("viewer",{})
    raise ValueError(f"Unknown tool: {name}")

async def main():
    async with stdio_server() as (r, w):
        await server.run(r, w, server.create_initialization_options())

if __name__ == "__main__":
    import asyncio; asyncio.run(main())
