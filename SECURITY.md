# Security policy

Please report vulnerabilities privately using GitHub's "Report a vulnerability" (Security Advisories) on this repository, not in public issues. Include affected version, impact and reproduction steps. Do not include real journal data.

Scope notes: the app is single-user and relies on encrypted disks and a trusted host/cluster; see the "Access model" section of `docs/ARCHITECTURE.md`. Reports about the password not encrypting SQLite at rest are known design limits.
