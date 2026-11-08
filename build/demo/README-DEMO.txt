YACM demo files
===============

This download is only for trying YACM without real network devices.
YACM itself does not need it; unpack it anywhere and delete it when done.

yacm-demo-device.exe
    A small pretend Cisco-like router. It answers SSH and telnet only on
    this computer (127.0.0.1), so nothing on the network can reach it.
    Start it by double-clicking; close its window (or press Ctrl+C) to stop.

      SSH                        127.0.0.1:2222
      SSH, old algorithms only   127.0.0.1:2224
      Telnet                     127.0.0.1:2323

      Login admin / cisco123, enable secret: secret

    After "enable", "reload" drops every session and keeps the ports closed
    for 20 seconds, like a real reload, so you can watch YACM wait for the
    device and reconnect.

    Its flash: is a folder in your user cache folder (the window shows
    where), with an SCP server, so you can try Files > Send file, Get file,
    Get show tech and the file browser. After "enable", "copy" works to and
    from YACM's file servers (Files > File servers... in the status bar), for
    example
      copy flash:startup-config tftp://127.0.0.1/startup-config

demo.yacm
    Example sessions for the demo device: credential profiles, a bastion
    (jump host), groups with defaults, a telnet session with login
    automation (also one through the bastion), the LAB tag, a session
    with a saved command and its own terminal colour, and a tunnel on the
    bastion (Tunnels in the status bar: 127.0.0.1:12323 reaches the demo
    telnet port through SSH). In YACM: the arrow next to + above the session tree >
    Import..., choose demo.yacm. Export password: demo-export-pw

The demo device is part of YACM (GPL-3.0-or-later); its source is in
cmd/yacm-demo-device in the YACM source code.
