# Security Policy

Soulacy ships an installer that can run with `curl | bash` and executes
LLM-driven tools, so we take security reports seriously.

## Supported versions

Security fixes are applied to the latest released version on the `main` branch.
Older tags are not maintained.

## Reporting a vulnerability

**Please do not open a public GitHub issue for security vulnerabilities.**

Instead, report privately using one of:

- GitHub's [private vulnerability reporting](https://github.com/vmodekurti/soulacy-personal/security/advisories/new)
  (Security → Report a vulnerability), or
- email the maintainer at the address listed on the GitHub profile for
  [`vmodekurti`](https://github.com/vmodekurti).

Please include:

- a description of the issue and its impact,
- steps to reproduce (proof-of-concept welcome),
- affected version / commit, and
- any suggested remediation.

## What to expect

- We aim to acknowledge reports within **5 business days**.
- We will confirm the issue, determine affected versions, and keep you updated
  on remediation progress.
- Once a fix is released, we are happy to credit you in the advisory and
  changelog unless you prefer to remain anonymous.

## Scope notes

Soulacy runs agent tools in a **lightweight** sandbox (resource rlimits only —
no filesystem, network, or namespace isolation). Running untrusted agents or
enabling `shell_exec` for untrusted input is outside the supported threat model.
See the sandbox documentation for the precise guarantees.
