# Security policy

## Reporting a vulnerability

Please **do not** open a public issue for a security problem.

Report it privately to the maintainers:

- Email: colin404@foxmail.com
- Or use GitHub's "Report a vulnerability" advisory flow.

Include: affected component and version, a description, reproduction steps, and the
impact you believe it has. We will acknowledge the report, investigate, and coordinate a
fix and disclosure timeline with you.

## Supported versions

The latest release on the `main` branch receives security fixes. Older tags are not
maintained unless stated otherwise.

## Handling secrets

- Never commit credentials, tokens, private keys, or internal endpoints.
- Use environment variables or a secret manager.
- If a secret was committed, rotate it first; removing it from the current tree does not
  remove it from git history.
