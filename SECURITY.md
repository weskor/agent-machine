# Security Policy

## Supported Versions

Pi Symphony is pre-`v0.1.0`. No public version is considered stable or supported
until the first tagged release is published.

## Reporting A Vulnerability

Use GitHub Security Advisories for private reports. Do not include credentials,
private keys, Linear issue data, target repository secrets, or exploit details in
public issues.

Include:

- affected version or commit;
- configuration shape needed to reproduce, with secrets redacted;
- expected impact;
- any safe reproduction steps.

## Secret Handling

Pi Symphony reads Linear, GitHub token, and GitHub App credentials from the
environment, `--env-file`, or local `.env.local` files. Never commit real tokens,
private keys, generated `.symphony/` state, or copied local environment files.
