# Security policy

Private vulnerability reporting is not configured, and there are no supported released versions. Do not disclose vulnerabilities in public issues. Before treating this project as a stable release, the maintainer should enable GitHub private vulnerability reporting (or document another private contact) and list supported versions here. Transparent mode remains experimental.

Do not report vulnerabilities in public issues or include production IPs/configs/logs. Once a reporting channel is announced, use it for parser flaws, source-spoofing/route issues, unsafe network cleanup, privilege escalation or denial-of-service findings.

## Current security boundaries

- Config and the installation path must be writable only by an administrator.
- Public Minecraft ingress should expose only the intended router TCP listener; backend direct access must be firewall-restricted.
- `online-mode=false` and disabled whitelist on a backend permit unauthenticated/identity-spoofed entry and are not fixed by source-IP preservation.
- Transparent mode is experimental and needs its own Linux network privilege/security review. It is not enabled in standard mode.
- See [docs/security.md](docs/security.md) for the local threat model.
