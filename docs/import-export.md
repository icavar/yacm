# Importing and exporting

How to bring sessions into YACM from other programs, device lists and
NetBox, and YACM's own `.yacm` format. The [user guide](guide.md) has
everything else.

- [Importing from other programs](#importing-from-other-programs)
- [CSV, Excel and NetBox](#csv-excel-and-netbox)
- [The .yacm export format](#the-yacm-export-format)

## Importing from other programs

The import/export button next to + offers *Import from a file…*, *Import a
sessions folder…* and every configuration found in its default location.
YACM shows how many sessions it found and whether saved passwords can be
read, then puts everything into a new group (with more than 30 sessions its
folders stay closed); large imports show their progress and can be
cancelled. Only SSH, telnet and serial sessions are imported; everything
else (RDP, VNC, SFTP, port forwards, proxies YACM does not support) is listed
in the report. Nothing in the other program is
changed. SSH sessions from other programs get the *Compatible* algorithm set,
because these configurations often point at older devices.

| Program | What to choose | Passwords |
|---|---|---|
| **SecureCRT** (Windows, macOS, Linux) | The `Config\Sessions` folder (`%APPDATA%\VanDyke\Config\Sessions`, `~/Library/Application Support/VanDyke/SecureCRT/Config/Sessions`, `~/.vandyke/SecureCRT/Config/Sessions`), or an XML export (File > Export Settings); the SecureCRT folder itself works too. Folders become groups. A jump session (Firewall = Session) becomes a jump host, and a named SOCKS5 or HTTP firewall from `Config/Firewalls` becomes the session's outbound proxy (its login is not taken over). Credential sets (`Config/Credentials`) become credential profiles, and sessions that use one get that profile, also when you import only a folder inside `Sessions`; importing again uses the profiles already there, and a set that is not among the imported files is taken from a profile of the same name you already have. Serial sessions from Windows, Linux and macOS keep their port and line settings. **Logon Actions** (Automate logon) become automation rules: expect texts are matched literally, values sent at a password prompt or marked Hide are kept hidden in the vault, the session's user name and password become `<USER>` and `<PASS>`, and "Send initial carriage return" is kept. Logon script files are listed in the report. | Old-style and "02"/"03" encrypted passwords, also those of credential sets. If SecureCRT uses a configuration passphrase, enter it in the import dialog and press **Check**. |
| **Xshell** 5–8 | The `Sessions` folder (`Documents\NetSarang Computer\<version>\Xshell\Sessions`) or single `.xsh` files. | Only on the **same Windows account** that saved them (Xshell ties them to the user name and SID), or with the Xshell master password if one is set. Keys from Xshell's internal key store are not exported by Xshell; those sessions are set to use the SSH agent. |
| **MobaXterm** | `MobaXterm.ini` (`%APPDATA%\MobaXterm` or next to the portable exe) or a `.mxtsessions` export. Folders become groups; jump hosts (SSH gateways) are kept. | Not imported (MobaXterm protects them with its own master password). YACM asks on first connect and can store them in the vault. |
| **mRemoteNG** | `confCons.xml` (`%APPDATA%\mRemoteNG`) or an exported XML file, including fully encrypted files. | Yes. If you set a custom password in mRemoteNG (Options > Security), enter it in the import dialog. |
| **PuTTY** | On Windows the sessions are read from the registry directly; a `.reg` export (`reg export HKCU\Software\SimonTatham\PuTTY\Sessions putty.reg`) and Linux/macOS `~/.putty/sessions` also work. SSH jump hosts (PuTTY 0.77+) are kept. | PuTTY does not store passwords. `.ppk` keys (format 2 and 3) are used as they are. |
| **Ásbrú Connection Manager** | `asbru.yml` (`~/.config/asbru`). Groups, SSH / Telnet / serial sessions, jump hosts, expect rules (including `<V:n>` and `<GV:name>` variables), local commands, macros (as saved commands), clusters (as tags) and log settings. Goto jumps in expect rules are listed in the report. | Yes (Ásbrú uses a fixed, publicly known key). |
| **OpenSSH** | `~/.ssh/config`, including `Include` files. Each concrete `Host` alias becomes a session; `Host *` defaults are applied; `ProxyJump` and `ProxyCommand ssh -W %h:%p` become jump hosts. `Match` blocks are ignored. | Not applicable (keys and agent are kept). |

The decryption routines follow publicly documented analyses of these formats
and are tested against the published reference implementations. If an import
from your own configuration goes wrong, the report says which session and why.

## CSV, Excel and NetBox

**Import a CSV or Excel list…** reads a `.csv` (comma, semicolon or tab
separated, as Excel writes it in any language) or the first sheet of an
`.xlsx` workbook. The first row must be the column names. YACM guesses which
column is the name, IP address, port, protocol, user name, password, group
(`Zagreb/DC1` makes nested groups), description, tags (column `Tags` or
`Clusters`), credential profile name, jump host (the name of a session),
serial port, baud rate and colour (English and Croatian headers are
recognised),
shows the first rows, and lets you change the mapping. Passwords from the
sheet are moved into the vault (and the report reminds you to delete the
sheet).

**Import from NetBox…** needs the address and an API token with read access
(NetBox 3.x and 4.x; `nbt_` v2 tokens are sent as Bearer tokens). It reads
devices, and optionally virtual machines, that have a primary IP address,
filtered by site, role and tag slugs and status. The address must use
`https://` (plain `http://` only for NetBox on this computer, so the token
never crosses the network in clear text). The token can be kept in the
vault; a kept token is only sent to the address it was saved with. Imported devices go into a new group whose defaults hold the chosen
credential profile and jump host, so changing the group changes every device.

Both imports can give the new group a credential profile, a jump host and a
user name for devices without one.

## The .yacm export format

Export with the import/export button next to + in the session list
(**Export sessions…**), or right-click a group, a session or a selection and
choose **Export…**. A `.yacm` file is plain UTF-8 JSON:

| Field | Content |
|---|---|
| `format`, `version` | `"yacm-sessions"` and the format version, now `2` (files of version 1 import too). A higher version is refused with a clear message. |
| `app`, `exportedAt`, `scope` | Who wrote it, when (UTC), and what was selected. |
| `nodes` | Groups and sessions, exactly as stored in `sessions.json`: connection settings, jump hosts, automation rules, local commands, tags (`clusters`), saved commands (`macros`), log and terminal options, colours, group defaults and tree order. |
| `credentials` *(version 2)* | The credential profiles the exported sessions use (user name, login method, key path). Their passwords go into `secrets` like all others. |
| `secrets` *(optional)* | Passwords, key passphrases and hidden automation values, encrypted with a separate **export password** (Argon2id + AES-256-GCM, same construction as the vault). Never written in clear text. |
| `knownHosts` *(optional)* | Trusted host keys of the exported SSH sessions and their jump hosts, plus `@cert-authority` lines. Public data. On import only plain keys for the hosts of the sessions in the file are added (see below). |
| `settings` *(optional)* | App settings (theme, font, scrollback, auto-lock, log folder). On import only the look is taken (theme, fonts, colours, highlighting, terminal habits). |

On import YACM shows what the file contains and asks:

- **Where**: into a new group, or at the top level keeping the file's structure.
- **Existing items** (same ID, e.g. a re-import or a restore): keep both
  (imported copies get new IDs), replace mine (content replaced, position
  kept), or skip them.
- **Export password**: leave it empty to import sessions without passwords.

When only part of the tree is exported, whatever the sessions inherit from
groups outside the export (login, jump host, algorithms, colour) is written
into them, so they behave the same after importing. A credential profile that
already exists on the importing computer is reused (or replaced in *Replace
mine* mode); a jump host session that is not in the file is dropped with a
warning.

Safety rules on import (a file may come from someone else):

- Local "before/after" commands from any imported file (also from other
  programs) are set to *ask first*.
- Trusted host keys are added only when you tick the box (off by default),
  only as plain keys for the hosts of the sessions in the file, and never over
  a key you already trust (also when written as `[host]:22`). Wildcards,
  hashed names, markers (`@cert-authority`, `@revoked`) and host names in a
  form the host key check cannot read (`[host]` without a port) are left
  out.
- Agent forwarding, the outbound proxy and the log folder and file name of
  sessions and groups in a `.yacm` file are left out and named in the
  report: set them again yourself for sessions you trust.
- Paths to files on other computers (`\\server\share\…`, also written as
  `\\.\UNC\…` or `\\?\UNC\…`) and serial devices other than a local port
  (`COM3`, `\\.\COM10`, `/dev/…`) are left out, also from CSV/Excel and
  NetBox: on Windows, opening one sends your login to that server. Key and certificate
  paths that do not exist on this computer are listed in the report.
- IDs are checked, and a reference to a credential profile that is not in the
  file, or that names one of your profiles with another name or user, is
  dropped (the session asks for a login). Sessions and groups that will log
  in with one of your existing profiles or groups are named in the report.
- In *Replace mine*, a session that now points to another address or through
  other jump hosts loses its saved passwords unless the file brings its own.
- App settings from the file only change the look; your security settings,
  saved commands and team libraries stay as they are.

Exporting with passwords needs your master password.

*Import from a file…* also accepts the files of the programs
below and detects the format automatically.
