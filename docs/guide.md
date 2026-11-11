# YACM user guide

The complete reference: everything YACM does, where to find it and how it
behaves. The [README](../README.md) has the overview; importing and the file
formats are in [import-export.md](import-export.md), security in
[security-compliance.md](security-compliance.md).

- [Getting started](#getting-started)
- [Sessions, groups and finding them](#sessions-groups-and-finding-them)
- [Logins](#logins)
- [Connections: SSH, telnet, serial, jump hosts](#connections-ssh-telnet-serial-jump-hosts)
- [Files and file servers](#files-and-file-servers)
- [Tunnels and proxies](#tunnels-and-proxies)
- [Working in the terminal](#working-in-the-terminal)
- [Session logs](#session-logs)
- [Team libraries](#team-libraries)
- [Your data](#your-data)
- [Security model](#security-model)
- [Trying it without hardware](#trying-it-without-hardware)
- [Shortcuts](#shortcuts)
- [Not supported](#not-supported)

## Getting started

On the first start YACM asks for a **master password** (at least 12
characters). It encrypts every password and passphrase you save; it is never
stored, and without it the saved passwords cannot be recovered. *Set up
later* starts without a vault; YACM then asks for passwords when connecting.

The welcome screen offers three ways in:

- **Import your sessions** from SecureCRT, Xshell, MobaXterm, mRemoteNG,
  PuTTY, Ásbrú, `~/.ssh/config`, a CSV or Excel list or NetBox
  ([details](import-export.md)). Configurations in their default locations
  are found automatically.
- **Add your team's library**, if a colleague has published one (see
  [Team libraries](#team-libraries)).
- **Try the demo device**, a separate download from the release page (see
  [Trying it without hardware](#trying-it-without-hardware)). When the demo
  is unpacked next to YACM, one click imports its sessions.

The bar on the left edge has the tools: Sessions (click again to fold the
list away for a wider terminal), Saved commands, Credential profiles and
File servers and tunnels; Settings and the vault padlock are at its bottom.
Hovering shows each label.

## Sessions, groups and finding them

- **Session tree**: groups, drag and drop, duplicate, an icon per connection
  type, and colours (red for production, green for the lab) shown on the tab,
  the terminal edge and in the tree. Ctrl/Shift+click selects several sessions
  to connect, edit, export or delete together.
- **Group defaults**: a group gives its sessions a credential profile, a jump
  host, an SSH algorithm profile, a colour and terminal colours. Sessions set
  to *From group* use them, also in subgroups, so moving a site behind a new
  bastion is one change.
- **Edit many sessions at once**: tick the settings to change (login, user,
  password, jump host, algorithms, port, colour, group, tags, logging, font,
  terminal colours, paste pause, anti-idle) and apply them to the selection or
  to a whole group.
- **One field to find and connect** (above the tree): typing part of a name,
  address, group or tag filters the tree. Enter connects to the session whose
  name or address is exactly what you typed (or the only match), opens a quick
  connect for an address nothing saved has (`admin@10.0.0.1:2222`,
  `telnet://10.1.1.1`, `serial://COM3?baud=9600`), or with several matches
  moves the choice into the tree. A line under the field says which it will
  be. A quick SSH connect uses the *Compatible* algorithm profile (post-quantum
  key exchange first, SHA-1 key exchange and `ssh-rsa` only if the device
  offers nothing better), so older gear connects without a saved session.
- **Go to session** (Ctrl+Shift+O, ⌘O on a Mac) ranks matches among
  thousands of sessions and lists the ones you opened last; Shift+Enter opens
  one next to the current tab.
- **Tags** are labels across folders (CORE in every site); clicking a tag
  opens all its sessions at once.
- **Favourites** (right click > *Add to favourites*) show at the top of the
  tree and first in Go to session, per computer. *Copy address* copies
  `user@host` of one or several sessions for a ticket or a chat.
- **Session name from the hostname**: after login YACM reads the hostname in
  the prompt (`core-sw1#`) and offers to rename a session that was created by
  IP address. It can also rename automatically or never (Settings > General,
  or *Keep this name* per session).

## Logins

- **Credential profiles**: one login (user name plus password, key file or
  agent) shared by many sessions. When your directory password changes,
  change it once in the profile and every session follows. Profiles also fill
  `<USER>` and `<PASS>` in telnet and serial automation.
- **The vault** keeps every password, passphrase and token encrypted (see
  [Security model](#security-model)). It locks after 15 idle minutes by
  default (Settings > Security) and when YACM closes; the padlock on the left
  edge shows its state. Showing a stored password asks for the master
  password again.
- **Login automation**: ordered wait-for/send rules with regular
  expressions, per-rule timeouts, *stop* or *skip* on timeout, hidden values
  kept in the vault, and the tokens `<USER> <PASS> <HOST> <PORT> <NAME>`. A
  rule with nothing to wait for sends at once, and "Press Enter once right
  after connecting" wakes up consoles and terminal servers. A one-click
  "Cisco enable login" preset is included.
- **Local commands** before connecting and after disconnecting (optionally
  confirmed), with `YACM_HOST`, `YACM_PORT`, `YACM_USER` and `YACM_NAME` set.

## Connections: SSH, telnet, serial, jump hosts

- **SSH** uses an embedded client (`golang.org/x/crypto/ssh`), so it behaves
  the same on every operating system:
  - three **algorithm profiles**: *Secure* (default; post-quantum
    `mlkem768x25519-sha256`, curve25519, AES-GCM, ChaCha20), *Compatible*
    (adds SHA-1 key exchange and `ssh-rsa` for IOS 15-era gear) and *Legacy*
    (adds CBC, 3DES and group1). A failed negotiation says which profile to
    try;
  - password, keyboard-interactive (including two-factor prompts), private
    keys in OpenSSH/PEM format or **PuTTY `.ppk`** (versions 2 and 3, no
    conversion), **OpenSSH user certificates** (`<key>-cert.pub` is picked up
    automatically) and the SSH agent (Windows OpenSSH Authentication Agent or
    `SSH_AUTH_SOCK`);
  - **known_hosts** in OpenSSH format with `@cert-authority` (host
    certificates), `@revoked` and hashed entries; unknown and changed keys are
    shown with their SHA256 fingerprints before anything is sent;
  - keepalives, agent forwarding (off unless enabled per session) and RFC 4335
    break.
- **Jump hosts**: pick a saved SSH session as the jump host by searching
  (favourites and sessions already used as jump hosts come first); its
  address, login and its own jump host are used, so chains work and a bastion
  is changed in one place. Hops can also be entered by hand. Each hop checks
  its own host key and negotiates its own algorithm profile. Telnet sessions
  can go through SSH jump hosts too (encrypted up to the jump host).
- **Telnet** (RFC 854 with ECHO, SGA, TERMINAL-TYPE and NAWS), clearly marked
  as unencrypted.
- **Serial consoles**: COM ports and `/dev/ttyUSB*`, USB adapters listed with
  their product name, any baud rate, parity and stop bits, and *Break* in the
  status bar for ROMMON and password recovery.
- **Reconnect after a reload**: when a session drops after `reload`,
  `reboot`, `request system reboot`, `install … activate` and the like, YACM
  tries the device every few seconds (through its jump host) and reconnects
  once it answers, for up to 30 minutes. Any tab can also *Wait for the device
  and reconnect* from its menu.
- **Anti-idle**: while you are not typing, YACM types `!` on the command line
  every chosen number of seconds, without Enter, so the device does not log
  the session out on exec-timeout. Before your next key the `!` are erased
  again, so your command starts on a clean line. It types only at a prompt
  ending in `>` or `#`, never at `[confirm]` or a password question and never
  while pasting or while automation runs. The text can be changed (`{enter}`
  adds Enter, for systems that count whole lines). Per session or as a group
  default.

## Files and file servers

- **Send file and Get file through jump hosts**: copy files between this
  computer and a device over the session's own SSH connection, through all
  its jump hosts, without staging them on the bastion. SFTP where the device
  has it (Linux, Junos, NX-OS), SCP with `dir` and `show file systems` on
  Cisco IOS and IOS XE. YACM checks the free space first, keeps the device's
  file name for downloads and compares MD5 checksums afterwards
  (`verify /md5`, `show file … md5sum`, `md5sum`, `file checksum md5`,
  `verify /md5 file:` on Arista EOS). Without an MD5 check it makes sure the
  device kept the file under its name and size.
- **Files tab** (right click an SSH session > *Browse files*, or *Files ▾* in
  the status bar): this computer on the left, the device on the right with
  its file systems, drag and drop, delete, rename and new folders (SFTP), and
  a list of transfers with speed and MD5 results. Opened from a terminal tab
  it uses that tab's connection, so there is no second login (MFA).
  - Cisco IOS takes one SSH channel per connection and closes the connection
    after each command; YACM reconnects for every command (over the same
    jump hosts, with the password that worked) and uses a connection of its
    own when the terminal holds the channel. Several transfers start in
    parallel; a device that takes one at a time gets them one after the
    other. A device that offers SFTP but never answers falls back to SCP
    after 5 seconds. Folder listings are read from Cisco IOS, IOS XE, NX-OS
    and Huawei VRP.
  - Network devices end a file name at a space, so YACM offers to send such
    files with underscores (`cbr copy.txt` as `cbr_copy.txt`).
- **Command output to a file** (*Files ▾ > Save command output to file…*):
  types one command into the open session (SSH, telnet or serial, with the
  rights the session has, enable mode included), answers `--More--` and the
  pagers of other vendors, and saves everything up to the next prompt, for
  example `show tech-support` into `tech-support_<hostname>_<date>.txt`. A
  list of common commands per vendor (Cisco, Arista, Juniper, Huawei, Linux)
  fills it in; the terminal shows one line saying where the output went.
- **File servers** (the server icon on the left edge): TFTP, HTTP and SCP
  servers on this computer for devices that copy by themselves
  (`copy tftp:`, `copy http:`, `copy scp:`), on one shared folder (by
  default its own, *YACM file servers* in Downloads, never Downloads or the
  home folder itself), with uploads and HTTP folder listing off until you
  switch them on, allowed device addresses, a generated SCP password and
  switching off after 15 minutes without transfers. An upload may be at most
  16 GB and always leaves 1 GB free on the disk; stopping a server also
  disconnects devices that are logged in to it.
  - The **command helper** writes the copy command for Cisco IOS / IOS XE,
    NX-OS and IOS XR, Arista EOS, Juniper Junos or Linux with the address the
    device reaches, and types it into the session in front (or another
    connected one). *Choose…* picks the file on this computer, or, for copies
    from the device, in a list of the device's file systems.
  - For a device behind a jump host it opens a **reverse tunnel** on the jump
    host, and a server you reach over SSH that cannot connect back to this
    computer (VPN, NAT) copies through a tunnel in that session.
  - TFTP answers from the port it listens on, so stateful filters on the
    device (Arista EOS) and NAT let the replies in. When a device reads the
    start of a file and stops before the real copy (Cisco IOS XE checks the
    image first), that is listed as the device's check, not as an error.
  - A file name with spaces goes into the command with underscores, and the
    servers send the file for it.
  - On Linux, ports below 1024 need administrator rights: for TFTP on port 69
    YACM offers to allow them once (the system asks for the password; the
    setting is kept in `/etc/sysctl.d/60-yacm-file-servers.conf`).

## Tunnels and proxies

- **Port forwards** per session: local (`-L`), remote (`-R`) and dynamic
  (`-D`, a SOCKS5, SOCKS4a and HTTP proxy), started when the session
  connects, with traffic and errors in *Tunnels* in the status bar, and
  tunnels for one connection only. Local tunnels listen on 127.0.0.1 unless
  you allow other computers. *File servers and tunnels* on the left edge
  lists the tunnels of all sessions, with Stop and Go to tab.
- **Outbound proxy**: reach the device, or its first SSH jump host, through
  a company SOCKS5 or HTTP proxy, for SSH and telnet sessions, with its login
  from a credential profile, per session or for a whole group.

## Working in the terminal

- **Alerts from background sessions**: neighbours going down, err-disabled
  ports, reloads and duplex mismatches in a session you are not looking at
  mark its tab and show a message with *Show*; on Windows the taskbar button
  flashes. Alerts that arrive while the message is up join it, so a reload
  that takes down neighbours in many sessions shows one message. *Turn off…*
  in the message stops alerts for that tab, for that kind of message or
  altogether. Links going down are coloured but do not alert (a reload would
  raise one in every session to its neighbours); any highlighting rule can be
  made an alert (at most one per rule and session every 30 seconds).
- **Highlighting**: `down`, `err-disabled`, syslog messages
  (`%LINK-3-UPDOWN`), IP and MAC addresses and interface names in colour. The
  rules are regular expressions, editable in Settings with a live preview,
  and can be turned off per session. *Highlight* in the status bar (or select
  text > *Highlight this*) colours up to five words or `/regular
  expressions/` in one session; *Keep as a highlighting rule* makes one
  permanent.
- **Type to many**: what you type goes to the open sessions you tick (an
  *All* box ticks them all; a session opened later never joins by itself),
  plus a one-line "send to the checked sessions" box.
- **Saved commands** (*Commands…* in the status bar, Ctrl+Shift+Space, or
  right click > *Send saved command*; the *Saved commands* icon edits the
  library): commands with `{placeholders}` asked for before sending, for
  example `show running-config interface {interface}`, sent to the active
  session or to every session in Type to many. A session's own commands come
  first. Select text > *Save as command* keeps it.
- **Split view**: two sessions side by side, stacked, or four in a grid. Drag
  a tab or pane title onto another pane to move or swap sessions, or use the
  tab menu or Ctrl+Shift+Alt+arrow (⌘⌥+arrow on a Mac).
- **Terminal colours** per session, per group and as the default, for example
  a dark red background for production. Colours the device sends and
  highlighting stay as they are.
- **Safe pasting**: a confirmation before pasting more than one line, an
  optional pause between pasted lines for slow consoles (globally and per
  session), and a right-click menu with Copy, Paste and *Copy and paste*. Right
  click can also paste at once (Settings > Terminal).
- **Break**: *Send break* in the tab and terminal menus and Ctrl+Break (⌘. on
  a Mac) send a serial line break, telnet BRK or SSH break request (for
  ROMMON and password recovery, also through console servers).
- **Scrolling**: Page Up and Page Down scroll the terminal, except while a
  full-screen program (`less`, `vi`, `top`) runs on the device; Shift+Page
  Up/Down always scroll. Find in scrollback, copy on select.
- **Font size per tab** with Ctrl+mouse wheel, Ctrl+Shift+Plus/Minus or *Aa*
  in the status bar (which also resets it or keeps it for the session); the
  font weight is in Settings > Appearance. *Clone session* in the tab menu
  opens a second connection to the same device.

## Session logs

- Session logs are text files, optionally with the date and time at the start
  of every line. New and imported sessions log by default (Settings > Session
  logs); a log is never overwritten.
- **File names from a template**: `{group}/{name}_{date}_{time}.log`, with
  `{host}`, `{user}`, `{protocol}`, `{path}` (one folder per group) and
  `{ticket}`, optionally per group or session. Log files end in `.log` or
  `.txt`; any other ending gets `.log` added.
- **Change or ticket numbers**: YACM can ask for a change or ticket number when
  connecting (for every session that logs, or only in groups that ask), so a
  log is easy to find for an audit.
- Passwords, keys and SNMP communities printed by `show running-config` or
  `show configuration` (IOS, NX-OS, ASA, Junos, EOS) are written as `*****`
  in the log; the terminal shows them as they are.

## Team libraries

A team library is a JSON file (`"format": "yacm-team-library"`) that a team
keeps on a share, in a synced folder or in git. Right-click one of your groups
and choose **Publish as team library…**; colleagues add the file under
**Settings > Team libraries > Add a library**.

- Followers see the library at the bottom of the session list, marked *team*.
  Its sessions connect, filter, open in split view and take part in tags like
  your own, but cannot be changed; **Copy to my sessions** makes an editable
  copy. A personal session can use a library session as its jump host.
- **Logins** in a library are named placeholders such as "AD account".
  Everyone links each one to a credential profile of their own (profiles with
  the same name are linked automatically, and *New profile with my account…*
  makes and links one); an unlinked login asks when connecting. The sessions
  of the published group should therefore log in with a credential profile
  (*Edit group and defaults > Login*); publishing warns about sessions that use
  their own user name instead.
- The file is checked every few seconds and reloaded when it changes. A copy is
  cached in the data folder, so an unreachable share still shows the last
  version (marked *offline*).
- **Editing**: *Edit this library* makes your own copy of it (logins mapped to
  your profiles); change it like any group, then *Publish changes*. Publishing
  takes a lock file, refuses to overwrite a revision someone else published
  after your copy was made (unless you confirm), and keeps the previous file
  in `.yacm-backup` next to it (the last 10). Login IDs stay the same, so
  colleagues' links keep working.
- **Redirected sessions**: YACM remembers where each library session pointed
  when you last connected (`team-seen.json`). If the address, port, protocol
  or jump host, its proxy, agent forwarding or what it sends by itself
  (automation rules, anti-idle text) have changed since, it shows the old and the new state and asks
  before sending your login. Whenever a library changes (also while YACM was
  closed), a list shows what was added, removed or changed, and who published
  it. Give write access to the share only to the library editors.
- **Your login stays an answer to your device's login prompt**: saved
  commands from a library never send `<PASS>` (and `<USER>` only as a line of
  its own). An automation rule from a library sends `<USER>` or `<PASS>` only
  as its whole answer with Enter, at a prompt written as plain text that ends
  like a user name or password prompt (`Password:`, `Username:`, `word:`),
  matched as plain text at the very end of the output after a moment of
  quiet (the same words in a banner, a log line or a file on screen do not
  count), and only while the rules before it sent
  nothing but logins and a few harmless commands (`enable`, `su`,
  `terminal length 0` …): after a rule that sends anything else (`telnet …`,
  `copy … scp://…`), login answers are skipped, so a library cannot make a
  device ask for your password on someone else's behalf. Library rules,
  saved commands and anti-idle texts never send control characters, and a
  library rule waits at most 30 seconds. *Copy to my
  sessions* switches such rules off and leaves such commands out.
- **Never in a library**: passwords, passphrases, key files and certificates,
  local commands (they would run on colleagues' computers), log folders,
  hidden automation values, proxy logins and agent forwarding. A library read from a file is cleaned of these
  again. A command library folder can go along (read-only in the command
  palette).
- Team members can run different YACM versions: a library published with a
  newer YACM can be used, but not edited or replaced, so nothing in it is lost
  for colleagues on the newer version.

## Your data

Sessions, credential profiles, commands and settings (`sessions.json`), the
vault (`vault.json`), trusted host keys (`known_hosts`) and session logs
(`logs`) are kept in one folder: `%AppData%\YACM` (Windows),
`~/Library/Application Support/YACM` (macOS) or `~/.config/YACM` (Linux).

- **Another folder**: Settings > Data and backups > *Change…* moves
  everything to another folder (another disk, a synced folder, a network
  share). The files are copied and checked before they are removed from the
  old folder; a folder that already has YACM data can be used as it is, or
  replaced (the old files are kept in a backup folder). YACM restarts to use
  the new folder. The default folder keeps a small `location.json` that points
  to it.
- If the chosen folder cannot be reached at start (disconnected disk, no VPN),
  YACM changes nothing and offers to try again, choose another folder or use
  the default one.
- **Two computers**: a `yacm.lock` file marks the folder as in use. YACM on a
  second computer opens it read-only and says who has it open; *Open anyway*
  exists for a computer that crashed. Starting YACM twice on one computer
  brings the open window to the front.
- **Backups**: sessions, profiles, settings, the vault and trusted host keys
  are copied into `backups` automatically (at most every 10 minutes while you
  work) and before imports, deleting, editing many sessions at once and
  changing the master password. The newest 10 and one from each of the last
  14 days are kept. Settings > Data and backups > *Backups* lists them;
  *Restore* saves the current state first, puts the backup back and restarts
  YACM. After a change of the master password the vault copies in the
  backups are encrypted with the new one, so the old password opens none of
  them.
- **Export and import** in YACM's own `.yacm` format, for backups, moving to
  another computer or sharing with colleagues; passwords only under a separate
  export password ([format](import-export.md#the-yacm-export-format)).
- **Portable use**: an empty file named `yacm.portable` next to the program
  keeps everything in a `yacm-data` folder beside it (USB stick).
  `YACM_DATA_DIR` sets the folder too; both take precedence over the setting.
- The default folder also keeps what belongs to this computer: the window
  placement, the sessions to reopen and `yacm.log` (plus `crash.log` if the Go
  runtime ever crashes). The window opens inside the visible part of the
  screen (never under the Windows taskbar) and remembers its size and
  position; Settings > General can reopen the sessions of last time.
- **Application log** (`yacm.log`, rotated at 1 MB): starts, failed
  connections and imports, errors and crashes, never passwords or session
  contents. Settings > About > *Copy diagnostics* puts versions, a few
  settings and the end of the log on the clipboard for a bug report.
- **Updates**: YACM never updates itself. Installed with Homebrew
  (`brew install --cask icavar/tap/yacm`), it is updated there; the Linux
  .deb and .rpm packages add the YACM package repository (pkg.yacm.app), so
  `apt upgrade` or `dnf upgrade` update them; the zip and tar.gz versions are
  replaced with the new download. The data folder stays as it is. An update check that asks GitHub once a day
  whether a newer version is out is off by default and only switched on in
  Settings > General; only that question is sent. Settings > About checks by
  hand.
- **Files from other versions**: new versions only add to the file formats.
  Settings, sessions and sections a newer YACM wrote into `sessions.json` are
  kept when an older one saves it. A session of a connection type this version
  does not know yet stays in the list, greyed and marked *newer YACM*, and
  cannot be opened or edited until YACM is updated. Importing a `.yacm` file
  from a newer YACM brings in what this version understands.
- On Linux YACM keeps a menu entry for itself
  (`~/.local/share/applications/yacm.desktop` and its icon), so the dock and
  the application list show the YACM icon; it follows the program when you
  move it, and an entry you wrote is left alone. Once the .deb or .rpm
  package is installed, the package's entry is used and YACM removes its own.

## Security model

The full picture for security officers and auditors is in
[security-compliance.md](security-compliance.md); vulnerabilities are
reported as described in [SECURITY.md](../SECURITY.md).

- Secrets (passwords, key passphrases, hidden expect values) live only in
  `vault.json`, encrypted with **AES-256-GCM**; the key is derived from your
  master password with **Argon2id** (t=3, m=64 MiB, p=4, RFC 9106). A vault
  made with PBKDF2-HMAC-SHA256 (at least 600,000 iterations) opens too. The
  file header is authenticated, so the key derivation parameters cannot be
  downgraded. The vault is never written in clear text.
- `sessions.json` holds no secrets and can be backed up or put under version
  control. Team library files hold no secrets either and cannot make YACM run
  local commands.
- YACM sends no telemetry; the update check is off unless you switch it on.
- Several programs store passwords so that anyone with their configuration
  file can decrypt them; the importers rely on exactly that. After importing,
  delete the old configuration files.
- Session logs are plain text; secrets in device configurations are masked in
  them by default.
- The interface runs with a strict Content Security Policy, loads nothing from
  the network, and opens links only on Ctrl+click.
- File servers run only while switched on (and stop after 15 minutes without
  transfers by default), serve their own folder without `..` escapes and
  without folder listings unless switched on, and the SCP server only copies
  files (no shell). TFTP and HTTP have no login: use them on lab and
  management networks, or limit the allowed addresses.
- File names from a device are used as plain names: a name with a path in it
  (`../../x`), or on Windows a device name such as `COM1` or `NUL`, is
  refused, so a download stays in the folder you chose.
- Links a device sends in its output open only on Ctrl+click, only for
  http(s), and after showing the address.
- Tunnels in a session from someone else's file (a `.yacm` import, a copy of a
  team library session) come switched off, and team library sessions never
  start tunnels by themselves: a port forward can expose this computer.

## Trying it without hardware

Each release has a separate demo download (`yacm-demo-device-*-windows.zip`,
`yacm-demo-device-*-macos.zip`, `yacm-demo-device-*-linux-amd64.tar.gz`;
YACM itself does not need it) with `yacm-demo-device` and `demo.yacm` (export password
`demo-export-pw`): example sessions with credential
profiles, a bastion, a group with defaults and login automation, all
pointing at the demo device.

The demo device is a small pretend router that listens only on 127.0.0.1:
SSH on 2222, SSH with old algorithms only (`diffie-hellman-group14-sha1`) on
2224 and telnet on 2323. Log in as `admin` / `cisco123`; the enable secret is
`secret`. It also works as a jump host for addresses on 127.0.0.1, so a session
on port 2224 (or the telnet one on 2323) can jump through the one on 2222.
After `enable`, `reload` drops every session and keeps the ports closed for 20
seconds (`-reload-time` changes it), to try reconnecting after a reload. Its
`flash:` is a folder in the user cache folder (`-flash` changes it), with an
SCP server, `dir`, `show file systems`, `verify /md5`, `delete`,
`show tech | redirect flash:…` and `copy` to and from YACM's file servers.
Like Cisco IOS it takes one SSH channel per connection and closes the
connection with it (`-one-channel=false` turns that off). Its SSH host key is
kept in the user cache folder, so YACM does not warn about a changed key after
a restart.

## Shortcuts

Shortcuts are Ctrl+Shift+key on Windows and Linux (so Ctrl+C, Ctrl+W and the
like stay with the device) and Cmd+key on macOS:

| Action | Windows and Linux | macOS |
|---|---|---|
| Copy / paste | Ctrl+Shift+C / V | ⌘C / ⌘V |
| Close tab | Ctrl+Shift+W | ⌘W |
| Find | Ctrl+Shift+F | ⌘F |
| Type to many | Ctrl+Shift+B | ⌘B |
| Reconnect | Ctrl+Shift+R | ⌘R |
| Go to session | Ctrl+Shift+O | ⌘O |
| Saved commands | Ctrl+Shift+Space | ⌘K |
| Font size | Ctrl+Shift+Plus / Minus / 0 | ⌘+ / ⌘− / ⌘0 |
| Break | Ctrl+Break | ⌘. |
| Move a session to another pane | Ctrl+Shift+Alt+arrow | ⌘⌥+arrow |

## Not supported

Copying whole folders, resuming an interrupted SFTP transfer, importing from
Termius (its sessions live in the cloud account, with no documented export)
and "goto" jumps between expect rules (an import lists them in its report).
RDP, VNC and SFTP sessions from other programs are not imported.
