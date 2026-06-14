# Security Policy

## Supported versions

containix is **experimental** and pre-1.0. Security fixes are applied to the latest tagged release on `main` only. Older tags are not back-ported.

| Version | Supported |
|---|---|
| Latest `containix/v*` release | ✅ |
| Older releases | ❌ |
| `main` branch (unreleased) | ⚠️ best-effort |

## Reporting a vulnerability

**Do not open a public GitHub issue for security reports.**

Use GitHub's [private security advisory](https://github.com/atmask/containix/security/advisories/new) feature instead. This keeps the report private until a fix is available and ready to disclose.

If you can't use the GitHub advisory flow, email **benjaminajm@gmail.com** with the subject line `containix security report`.

## What to include

- A description of the vulnerability and its impact.
- Steps to reproduce, ideally with a minimal proof-of-concept.
- The containix version (or commit SHA) you tested against.
- Any suggested mitigations or fixes, if you have them.

## Response process

- **Acknowledgement** within 5 business days.
- **Initial assessment** within 14 days — severity, scope, and a rough fix timeline.
- **Coordinated disclosure** — we'll work with you on a public disclosure date that gives users time to upgrade. Credit attributed unless you'd prefer to stay anonymous.

## Scope

In-scope:
- Sandbox escapes from a containix-managed container into the host.
- Privilege escalation via the shim binary.
- Arbitrary code execution via malicious flake annotations.
- Information disclosure of host secrets to a container.

Out-of-scope (report as regular issues instead):
- Resource exhaustion in the Nix daemon (operator concern).
- Bugs that require a malicious operator with root on the node already.
- Issues in upstream dependencies (containerd, runc, Nix) — please report those to their maintainers.

## Acknowledgements

containix's security posture inherits heavily from [containerd](https://github.com/containerd/containerd), [runc](https://github.com/opencontainers/runc), and [Nix](https://nixos.org). Vulnerabilities in those projects can affect containix; we track their advisories and update dependencies promptly.
