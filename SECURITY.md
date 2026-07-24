# Security Policy

## Reporting a vulnerability

Please **do not open a public issue** for security vulnerabilities.

Instead, use GitHub's private vulnerability reporting ("Security" tab → "Report a vulnerability") or email the maintainer. You can expect an acknowledgment within a few days.

Please include:

- A description of the issue and its impact
- Steps to reproduce (a proof of concept helps enormously)
- Affected component (auth, upload pipeline, crypto, sharing, …)

## Scope

Strato's security design — threat model, encryption architecture, token lifecycle, and known limitations — is documented in [docs/security.md](docs/security.md). Reports about the explicitly listed known limitations are still welcome if you can demonstrate impact beyond what's documented.

## Supported versions

The `main` branch. This is a portfolio/reference project without LTS branches; fixes land on `main`.
