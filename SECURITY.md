# Security

YACM stores credentials for network devices, so security problems are taken
seriously.

## Reporting a vulnerability

Please do not open a public issue. Use GitHub's private vulnerability
reporting (Security tab > Report a vulnerability) or write to
**security@yacm.app**. Include the YACM version, the steps to reproduce and
the impact you see.

- You get an answer within 5 working days.
- A confirmed problem is fixed in a new release as soon as possible; for a
  serious one we aim at 30 days. The release notes and a GitHub security
  advisory describe it, and you are credited unless you prefer not to be.
- Please give us the time to release a fix before you publish details.

## Supported versions

Only the latest release gets security fixes while YACM is below 1.0.
Release candidates (`-rc.N`) are for testing and get no fixes of their own.

## Design in short

- Passwords, key passphrases and tokens are kept in `vault.json`, encrypted
  with AES-256-GCM under a key derived from the master password with
  Argon2id. `sessions.json` holds no secrets.
- Exported `.yacm` files with passwords are encrypted with a separate export
  password.
- Team libraries never contain secrets; each user maps their own logins.
- YACM sends no telemetry and contacts no server except the devices, jump
  hosts and services (such as NetBox) you configure. The optional update
  check (off unless you switch it on in Settings) asks GitHub for the list of
  releases and sends nothing else.
- Secure defaults: the vault locks after 15 idle minutes, secrets in session
  logs are masked, agent forwarding is off, unknown and changed host keys are
  asked about, tunnels from someone else's files come switched off, and
  the file servers accept uploads only when you switch that on.
- Releases come with checksums, a software bill of materials (SBOM) and
  build provenance; the macOS app is signed and notarised.

See also docs/security-compliance.md.
