# Security Policy

## Supported Versions

| Version | Supported |
| ------- | --------- |
| latest release | ✅ |
| older releases | ❌ |

## Reporting a Vulnerability

Please **do not open a public issue** for security vulnerabilities.

Instead, report privately via [GitHub Security Advisories](https://github.com/aryasoni98/alertkube/security/advisories/new).

Include:

- A description of the vulnerability and its impact
- Steps to reproduce (proof of concept if possible)
- Affected version(s) / commit

You can expect an acknowledgement within 72 hours and a status update within 7 days. Fixes for confirmed vulnerabilities are released as soon as practical, with credit to the reporter unless anonymity is requested.

## Scope notes

alertkube holds credentials for external services (Slack webhook URLs, PagerDuty routing keys, Teams webhook URLs, generic webhook signing secrets). Reports involving credential exposure — in logs, metrics, alert payloads, or error messages — are treated as high severity. Webhook URLs are sanitized from logs by design (`internal/httpx`); regressions there are in scope.

The controller runs with read-only cluster RBAC. Any path that could be abused to escalate beyond the documented RBAC scope is in scope.
