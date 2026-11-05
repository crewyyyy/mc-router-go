# Security policy

GitHub private vulnerability reporting is enabled for this repository. Report suspected vulnerabilities through the repository's **Security** tab using private reporting; do not disclose them in public issues. There are no supported released versions yet. Supported versions and response expectations will be listed here when releases are published. Transparent mode remains experimental.

Do not report vulnerabilities in public issues or include production IPs/configs/logs. Once a reporting channel is announced, use it for parser flaws, source-spoofing/route issues, unsafe network cleanup, privilege escalation or denial-of-service findings.

## Current security boundaries

- Config and the installation path must be writable only by an administrator.
- Public Minecraft ingress should expose only the intended router TCP listener; backend direct access must be firewall-restricted.
- `online-mode=false` and disabled whitelist on a backend permit unauthenticated/identity-spoofed entry and are not fixed by source-IP preservation.
- Transparent mode is experimental and needs its own Linux network privilege/security review. It is not enabled in standard mode.
- See [docs/security.md](docs/security.md) for the local threat model.
