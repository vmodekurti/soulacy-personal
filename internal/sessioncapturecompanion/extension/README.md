# Soulacy Secure Session Capture

This companion lets a signed-in Soulacy user capture a reusable website
session without a terminal. It requests Chrome access only to the website
domain approved in Soulacy, opens the normal login page for MFA/CAPTCHA, and
returns cookies plus local storage directly to the Soulacy page. Passwords are
never read or stored by the extension.

## Install

1. Unzip the downloaded archive.
2. Open `chrome://extensions`, enable **Developer mode**, and select
   **Load unpacked**.
3. Choose the `soulacy-session-capture` folder.
4. Return to Soulacy. If that tab was open before the extension was installed,
   reload it once. The bridge then activates automatically on `*.soulacy.io`
   and local Soulacy installations.

If Soulacy still reports that the companion is not detected, select the
extension from Chrome's toolbar and connect it to the active Soulacy tab.

For managed Chrome fleets, deploy this folder as a pinned enterprise extension.
Administrators hosting Soulacy on a custom hostname must add that exact origin
to `content_scripts.matches` before packaging the managed extension. Do not use
a wildcard for unrelated sites—the bridge can initiate capture requests.
