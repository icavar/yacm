# YACM security and compliance

This document is for security officers, auditors and procurement teams who
need to decide whether YACM may be used to administer network devices. It
describes what YACM does with credentials and data, which security controls
it has, how it is built and released, and how its features help an
organisation meet the requirements of **NIS2**, **ISO/IEC 27001:2022**,
**PCI DSS v4.0** and the **EU Cyber Resilience Act (CRA)**.

YACM is not certified against any of these standards. Compliance is a
property of an organisation and its processes, not of a tool: YACM *helps
you meet* these requirements when it is used as described here. Every
statement below can be checked in the source code, which is public under the
GNU GPL.

## What YACM is

YACM (Yet Another Connection Manager) is a desktop application for Windows,
macOS and Linux that opens SSH, telnet and serial console sessions to network
devices and servers. It runs entirely on the user's computer. There is no
YACM server, no cloud service and no account.

## Data and where it is kept

| Data | Where | Protection |
|---|---|---|
| Passwords, key passphrases, hidden automation values, API tokens | `vault.json` in the data folder | AES-256-GCM; key from the master password with Argon2id (t=3, m=64 MiB, p=4, RFC 9106) or PBKDF2-HMAC-SHA256 (≥ 600,000 iterations). Header authenticated against downgrade. Never written in clear text. |
| Sessions, groups, credential profiles (without secrets), settings | `sessions.json` | No secrets; can be kept in version control. |
| Trusted host keys | `known_hosts` (OpenSSH format) | Public data. |
| Session logs (if enabled) | `logs/` | Plain text chosen by the user; passwords, keys and SNMP communities shown by `show running-config` and similar are masked by default. |
| Application log | `yacm.log` | Starts, errors, failed connections and imports. Never passwords or session contents. |
| Team library files | A share, synced folder or git repository chosen by the team | Never contain secrets; each user maps the library's logins to their own credential profiles. |
| Exports (`.yacm`) | Where the user saves them | Secrets only under a separate export password (same encryption as the vault). |

The data folder can be moved (another disk, an encrypted volume, a network
share); a lock file stops two computers from writing it at the same time.

## Network connections

YACM connects only to:

- the devices, jump hosts and proxies configured in the sessions;
- services the user configures (NetBox, team library locations);
- **GitHub (api.github.com), only if the user enables the update check**. It
  is off by default, YACM does not offer it by itself, and the user switches
  it on in Settings > General.
  The request contains no data about the user, the sessions or the devices.

YACM sends **no telemetry** and no crash reports. File servers (TFTP, HTTP,
SCP) and tunnels listen only while the user switches them on.

## Security controls

### Authentication and credentials

- Credentials are stored only in the encrypted vault; the master password is
  never stored.
- New master passwords and export passwords must have at least 12
  characters.
- The vault **locks after 15 idle minutes** by default (configurable) and
  when YACM closes. Showing a stored password asks for the master password
  again.
- **Credential profiles** let a team use personal accounts (for example
  Active Directory/TACACS+ logins) instead of shared device passwords; a
  password change is made once per profile.
- SSH public key authentication, SSH agents, `.ppk` keys and SSH
  certificates are supported.

### Secure connections

- SSH algorithm profiles: *Secure* (default; post-quantum hybrid key exchange
  `mlkem768x25519-sha256`, curve25519, AES-GCM and ChaCha20-Poly1305),
  *Compatible* and *Legacy* only for sessions that need them.
- **Host key verification**: an unknown key is shown with its fingerprint
  before the first connection; a changed key is a warning that names the old
  key. `@cert-authority` (host certificates) is supported.
- Every hop of a jump host chain is verified separately.
- Agent forwarding is off unless enabled per session.

### Secure defaults

- Vault auto-lock after 15 minutes; masking of secrets in session logs on;
  update check off until the user switches it on.
- File servers accept uploads only when switched on, stop after 15 minutes
  without transfers, serve their own folder without `..` escapes and without
  folder listings unless switched on, can be limited to allowed device
  addresses, and the SCP server only copies files (no shell).
- Tunnels in sessions that come from someone else's file (an import, a team
  library) are switched off until the user turns them on; team library
  sessions never start tunnels or run local commands by themselves.
- Files from someone else (imports, `.yacm` files, team libraries) cannot
  run local commands without asking, add trusted host keys beyond plain keys
  for their own sessions' hosts, turn on agent forwarding, send logins
  through a proxy of their choosing, choose where session logs are written,
  point at files on other computers, borrow
  stored secrets or credential profiles through crafted IDs, or put the
  user's login into a command: in team library automation and saved
  commands, `<USER>` and `<PASS>` only answer login prompts. Changes to a
  team session's address, jump hosts, proxy, agent forwarding or automation
  are shown and asked about before the next login.
- Exporting passwords needs the master password.
- Pasting more than one line into a session asks first.

### Logging and accountability

- Per-session logs with timestamps and a file name template that can carry
  the change or ticket number of the work (`{ticket}`).
- An application log of starts, failed connections, imports and errors.

### Application security

- The user interface runs with a strict Content Security Policy and loads
  nothing from the network; links open only on Ctrl+click.
- Files from other YACM versions keep members they do not know but never
  carry unknown members from someone else's file into the user's sessions.

## How YACM is developed and released

- **Source code** is public (GPL-3.0-or-later); every change is reviewed in
  the repository history.
- **Continuous integration** on Linux, Windows and macOS: unit, UI and
  end-to-end tests (including a container lab with jump hosts and network
  devices), the race detector, fuzzing of every parser, `govulncheck` for
  known vulnerabilities in Go code and modules, `npm audit` for the
  frontend, and a licence check of all dependencies.
- **Dependencies** are updated through Dependabot with a cool-down period;
  GitHub Actions are pinned to exact commits.
- **OpenSSF Scorecard** checks the repository's practices weekly (badge in
  the README).
- **Releases** are built by GitHub Actions from a tagged commit and come
  with SHA-256 checksums, a software bill of materials (SBOM, SPDX JSON) and
  build provenance attestations (`gh attestation verify <file> --repo
  icavar/yacm`). The macOS application is signed with an Apple Developer ID
  and notarised; signing of the Windows builds is being set up.
- **Vulnerability handling**: reports go to security@yacm.app or GitHub's
  private vulnerability reporting (SECURITY.md). Reports are answered within
  5 working days; serious problems are fixed as soon as possible, with a
  target of 30 days, and published as GitHub security advisories.

## Mapping to requirements

The tables show which YACM features support a requirement. They do not
replace the organisation's own controls.

### NIS2 (Directive (EU) 2022/2555, Article 21(2))

| Measure | How YACM helps |
|---|---|
| (d) Supply chain security | Public source, SBOM and build provenance for every release, signed macOS builds, dependency and licence checks. |
| (e) Security in acquisition, development and maintenance, incl. vulnerability handling | Published vulnerability process (SECURITY.md), CI with `govulncheck` and fuzzing, security advisories. |
| (g) Basic cyber hygiene | Secure defaults, host key verification, no shared passwords needed (credential profiles). |
| (h) Cryptography and encryption | AES-256-GCM vault with Argon2id; SSH with modern and post-quantum algorithms by default. |
| (i) Access control | Personal accounts through credential profiles, vault auto-lock, master password re-entry for revealing secrets. |
| (j) Multi-factor authentication | SSH keys, agents and certificates; YACM works with MFA on jump hosts (keyboard-interactive prompts). |

### ISO/IEC 27001:2022, Annex A

| Control | How YACM helps |
|---|---|
| 5.15 Access control, 5.18 Access rights | Credential profiles per person; team libraries without shared secrets. |
| 5.17 Authentication information | Encrypted vault, 12-character minimum, auto-lock, secrets never in configuration files or libraries. |
| 8.2 Privileged access rights | Enable passwords and automation values kept in the vault and hidden in logs. |
| 8.5 Secure authentication | Host key verification, SSH keys and certificates, modern algorithms by default. |
| 8.15 Logging | Session logs with timestamps and change numbers; application log. |
| 8.20 Networks security, 8.21 Security of network services | Jump host chains verified hop by hop, tunnels off by default for foreign files, file servers limited to allowed addresses. |
| 8.24 Use of cryptography | AES-256-GCM, Argon2id, SSH algorithm profiles. |
| 8.28 Secure coding, 8.29 Security testing | Public CI with tests, fuzzing, race detector, vulnerability scanning. |
| 8.8 Management of technical vulnerabilities | Vulnerability process and opt-in update check. |

### PCI DSS v4.0

| Requirement | How YACM helps |
|---|---|
| 2.2.7 Non-console administrative access is encrypted | SSH with strong cryptography; telnet only where the user chooses it. |
| 8.2.8 Re-authenticate after 15 minutes idle | Vault locks after 15 idle minutes by default. |
| 8.3.2 Strong cryptography for authentication factors in transmission and storage | Vault encryption, SSH. |
| 8.3.6 Passwords at least 12 characters | Minimum for the master and export passwords. |
| 10.2 Audit logs | Session logs with timestamps (to be kept by the organisation's log management). |
| 6.3.3 Security patches | Opt-in update check, published security releases. |

### EU Cyber Resilience Act (Regulation (EU) 2024/2847)

YACM is free and open-source software developed outside a commercial
activity. It nevertheless follows the CRA's essential requirements where
they apply to a tool like this: secure by default configuration (Annex I,
part I), protection of stored credentials, minimal data processing (no
telemetry), a coordinated vulnerability disclosure policy and a single point
of contact (security@yacm.app), security updates, and an SBOM for every
release (Annex I, part II).

## What YACM does not do

To plan compensating controls, note that YACM currently has:

- no central policy management (settings are per user);
- no tamper-proof audit log of commands beyond the session logs;
- no hardware-backed vault unlock (Windows Hello, Touch ID, FIDO2);
- no FIPS 140-3 validated cryptographic module.

## Contact

Security reports: security@yacm.app. Other questions: support@yacm.app or
GitHub issues.
