# Security Policy

## Reporting a Vulnerability

If you believe you've found a security vulnerability, **please do not open a
public issue**. Instead, email:

> security@<your-domain>.tld  *(placeholder — to be configured)*

Include:

- A description of the issue
- Steps to reproduce
- The version / commit hash
- Your name and contact details (for disclosure credit, if desired)

## What to Expect

| Step                    | Target time |
| ----------------------- | ----------- |
| Initial acknowledgement | 48 hours    |
| Triage and severity     | 5 days      |
| Fix and patch release   | depends     |
| Public disclosure       | coordinated |

We follow [coordinated disclosure](https://en.wikipedia.org/wiki/Coordinated_disclosure)
practices and will credit reporters who wish to be named.

## Scope

**In scope:**

- Authentication and authorization bypasses
- Data exposure (cross-tenant, audit log, secrets)
- Remote code execution
- Cryptographic flaws in the WireGuard control or signaling path
- Privilege escalation in the controller, agent, or web UI

**Out of scope:**

- Self-hosted deployments where the operator misconfigured TLS or authentication
- Issues requiring physical access to a peer device
- Denial of service from a single legitimate account

## Bug Bounty

We plan to launch a bug bounty program. Until then, we will recognize valid
reports in an acknowledgements file.
