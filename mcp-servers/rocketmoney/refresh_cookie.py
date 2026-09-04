#!/usr/bin/env python3.11
"""
Rocket Money cookie refresh utility.

Usage:
  python3.11 refresh_cookie.py          # extract from Chrome (must be logged in)
  python3.11 refresh_cookie.py --clear  # delete saved cookie to force re-extract

After running, restart Soulacy so the MCP server picks up the new cookie:
  lsof -ti :18789 | xargs kill -9 2>/dev/null; sleep 2
  cd ~ && nohup soulacy serve > ~/.soulacy/logs/soulacy.log 2>&1 &
"""
import os, sys

SECRETS_FILE = os.path.expanduser("~/.soulacy/secrets/rocket_money_cookie")

if "--manual" in sys.argv:
    print("Paste your auth_verification cookie value below, then press Enter:")
    val = input("> ").strip()
    if len(val) < 100:
        print(f"❌ Value too short ({len(val)} chars) — doesn't look like a valid cookie.")
        sys.exit(1)
    os.makedirs(os.path.dirname(SECRETS_FILE), exist_ok=True)
    with open(SECRETS_FILE, "w") as f:
        f.write(f"auth_verification={val}")
    os.chmod(SECRETS_FILE, 0o600)
    print(f"✅ Cookie written ({len(val)} chars). Restart Soulacy to pick it up.")
    sys.exit(0)

if "--clear" in sys.argv:
    if os.path.exists(SECRETS_FILE):
        os.remove(SECRETS_FILE)
        print("Cookie file deleted. Next MCP call will try Chrome.")
    else:
        print("No cookie file to delete.")
    sys.exit(0)

# Extract from Chrome
try:
    import browser_cookie3
except ImportError:
    print("ERROR: browser_cookie3 not installed.")
    print("Run: python3.11 -m pip install --break-system-packages browser-cookie3")
    sys.exit(1)

domains = [
    ".rocketmoney.com", "rocketmoney.com",
    "auth.rocketmoney.com", "truebill.auth0.com",
    "client-api.rocketmoney.com", "app.rocketmoney.com",
]
all_cookies = {}
for domain in domains:
    try:
        for c in browser_cookie3.chrome(domain_name=domain):
            all_cookies[c.name] = c.value
    except Exception:
        continue

auth_val = all_cookies.get("auth_verification", "")
if not auth_val or len(auth_val) < 100:
    if not auth_val:
        print("❌ browser_cookie3 could not find auth_verification.")
    else:
        print(f"⚠️  auth_verification only {len(auth_val)} chars — looks expired.")
    print()
    print("This is a macOS Keychain decryption issue with browser_cookie3.")
    print("Use the manual method instead:")
    print()
    print("  1. Open Chrome → app.rocketmoney.com (log in if needed)")
    print("  2. Press F12 → Application tab → Cookies → https://app.rocketmoney.com")
    print("  3. Find 'auth_verification', double-click the Value column, copy it")
    print("  4. Run:  python3.11 ~/.soulacy/mcp-servers/rocketmoney/refresh_cookie.py --manual")
    print()
    sys.exit(1)

cookie_str = "; ".join(f"{k}={v}" for k, v in all_cookies.items())
os.makedirs(os.path.dirname(SECRETS_FILE), exist_ok=True)
with open(SECRETS_FILE, "w") as f:
    f.write(cookie_str)
os.chmod(SECRETS_FILE, 0o600)

print(f"✅ Cookie written: {len(cookie_str)} bytes, {len(all_cookies)} cookies")
print(f"   auth_verification: {len(auth_val)} chars")
print()
print("Now restart Soulacy:")
print("  lsof -ti :18789 | xargs kill -9 2>/dev/null; sleep 2")
print("  cd ~ && nohup soulacy serve > ~/.soulacy/logs/soulacy.log 2>&1 &")
