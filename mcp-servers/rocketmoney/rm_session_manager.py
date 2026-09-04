#!/usr/bin/env python3.11
"""
Rocket Money session manager — Playwright-based.

First-time setup (run once, interactive):
  python3.11 rm_session_manager.py --setup

Auto-refresh (headless, called by server_core.py on 401):
  python3.11 rm_session_manager.py --refresh

The setup opens a real browser window where you log in normally (including 2FA).
The session state (cookies + localStorage) is saved. After that, --refresh
loads the saved state in a headless browser, visits the Rocket Money dashboard to
let the server renew the session token, and writes fresh cookies to the secrets
file. 2FA is only needed when the saved state completely expires (usually months).
"""
import os, sys, json, subprocess

SECRETS_FILE  = os.path.expanduser("~/.soulacy/secrets/rocket_money_cookie")
STATE_FILE    = os.path.expanduser("~/.soulacy/secrets/rm_playwright_state.json")
RM_URL        = "https://app.rocketmoney.com"
DASHBOARD_URL = "https://app.rocketmoney.com/overview"


def _write_cookie_file(cookies: list[dict]) -> int:
    """Write cookies list (Playwright format) to the secrets file."""
    rm_cookies = [c for c in cookies if any(
        d in c.get("domain", "") for d in ["rocketmoney", "truebill", "auth0"]
    )]
    if not rm_cookies:
        return 0
    cookie_str = "; ".join(f"{c['name']}={c['value']}" for c in rm_cookies)
    os.makedirs(os.path.dirname(SECRETS_FILE), exist_ok=True)
    with open(SECRETS_FILE, "w") as f:
        f.write(cookie_str)
    os.chmod(SECRETS_FILE, 0o600)
    return len(rm_cookies)


def _check_auth(cookies: list[dict]) -> bool:
    """Return True if cookies contain any known Rocket Money session indicator."""
    session_cookies = {
        "auth_verification",   # legacy Truebill session token
        "tb.auth0.sid",        # current Auth0 session ID
        "auth0.is.authenticated",
    }
    for c in cookies:
        if c.get("name") in session_cookies and len(c.get("value", "")) > 20:
            return True
    return False


def setup():
    """
    Interactive one-time setup.
    Opens a visible browser, navigates to Rocket Money, waits for the user to log
    in fully (including 2FA), then saves the session state.
    """
    try:
        from playwright.sync_api import sync_playwright
    except ImportError:
        print("Playwright not installed. Run:")
        print("  python3.11 -m pip install playwright --break-system-packages")
        print("  python3.11 -m playwright install chromium")
        sys.exit(1)

    print("Opening browser for one-time Rocket Money login...")
    print("Log in normally — including any 2FA prompts.")
    print("Once you can see your dashboard, come back here and press Enter.")
    print()

    with sync_playwright() as p:
        browser = p.chromium.launch(headless=False, slow_mo=50)
        context = browser.new_context(
            viewport={"width": 1280, "height": 800},
            user_agent="Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36",
        )
        page = context.new_page()
        page.goto(RM_URL, wait_until="domcontentloaded")

        input("Press Enter once you're on the Rocket Money dashboard > ")

        state = context.storage_state()
        cookies = state.get("cookies", [])

        # Show all cookies so we can debug / identify the right auth cookie
        rm_cookies = [c for c in cookies if any(d in c.get("domain","") for d in ["rocketmoney","truebill","auth0"])]
        print(f"\nCookies found on Rocket Money domains ({len(rm_cookies)}):")
        for c in rm_cookies:
            print(f"  {c['name']}: {len(c.get('value',''))} chars  [domain: {c['domain']}]")

        if not rm_cookies:
            print("\n⚠️  No Rocket Money cookies found at all.")
            print("   Make sure you're fully logged in and on the dashboard, then re-run --setup.")
            context.close()
            browser.close()
            sys.exit(1)

        # Accept any session — we'll find the right auth cookie name from the list above
        os.makedirs(os.path.dirname(STATE_FILE), exist_ok=True)
        with open(STATE_FILE, "w") as f:
            json.dump(state, f)
        os.chmod(STATE_FILE, 0o600)

        n = _write_cookie_file(cookies)
        context.close()
        browser.close()

    print(f"\n✅ Session saved ({n} Rocket Money cookies).")
    print("   Future refreshes will be fully headless — no browser window needed.")


def refresh() -> bool:
    """
    Headless refresh using the saved Playwright session state.
    Returns True on success, False if the saved state has expired (needs re-setup).
    """
    if not os.path.exists(STATE_FILE):
        print("No saved session. Run: python3.11 rm_session_manager.py --setup", file=sys.stderr)
        return False

    try:
        from playwright.sync_api import sync_playwright, TimeoutError as PWTimeout
    except ImportError:
        print("Playwright not installed.", file=sys.stderr)
        return False

    try:
        with sync_playwright() as p:
            browser = p.chromium.launch(headless=True)
            context = browser.new_context(
                storage_state=STATE_FILE,
                user_agent="Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36",
            )
            page = context.new_page()

            # Navigate to dashboard — server will refresh session token automatically
            page.goto(DASHBOARD_URL, wait_until="domcontentloaded", timeout=20_000)

            # Check we landed on the dashboard, not the login page
            current = page.url
            if "login" in current or "auth" in current or "signin" in current:
                context.close()
                browser.close()
                print("Session expired — re-run setup: python3.11 rm_session_manager.py --setup",
                      file=sys.stderr)
                return False

            state   = context.storage_state()
            cookies = state.get("cookies", [])

            if not _check_auth(cookies):
                context.close()
                browser.close()
                print("Auth cookie missing after refresh — session likely expired.", file=sys.stderr)
                return False

            # Persist updated state so next refresh benefits from any token rotation
            with open(STATE_FILE, "w") as f:
                json.dump(state, f)

            n = _write_cookie_file(cookies)
            context.close()
            browser.close()

        print(f"✅ Cookie refreshed ({n} cookies).", file=sys.stderr)
        return True

    except Exception as e:
        print(f"Refresh failed: {e}", file=sys.stderr)
        return False


if __name__ == "__main__":
    if "--setup" in sys.argv:
        setup()
    elif "--refresh" in sys.argv or len(sys.argv) == 1:
        ok = refresh()
        sys.exit(0 if ok else 1)
    else:
        print(__doc__)
