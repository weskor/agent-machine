# Security Policy

## Supported Versions

The latest `v0.1.x` release receives security fixes. Earlier releases should be
upgraded before filing support requests.

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

Agent Machine reads Linear, GitHub token, and GitHub App credentials from the
environment, `--env-file`, or local `.env.local` files. Never commit real tokens,
private keys, generated `.am/` state, or copied local environment files.
