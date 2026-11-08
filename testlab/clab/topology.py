#!/usr/bin/env python3
"""Writes the topologies of the YACM test lab (one source for both):

  yacm.clab.yml        8 Arista cEOS (2 edges, 2 spines, 4 leaves) with OSPF
  yacm-linux.clab.yml  the same with Linux routers instead of cEOS, for
                       machines without the cEOS image (a GitHub runner)

Run it after changing the lab: python3 topology.py
"""

MGMT = "172.30.0"

# Containerlab gives every link an MTU of 9500, cEOS routes at 1500. Linux
# nodes left at 9500 send frames that cEOS drops without an ICMP "fragmentation
# needed", so large SSH packets (bulk transfers, some key exchanges) vanish.
# Every link runs at 1500, as in a real network.
MTU = 1500

# Linux nodes: name -> (roles, mgmt address or None, [(iface, address)], routes, extra)
LINUX = {
    "bastion1": ("gateway", f"{MGMT}.11", [("eth1", "10.0.1.1/30")], []),
    "bastion2": ("gateway", None, [("eth1", "10.0.1.2/30"), ("eth2", "10.0.2.1/30")], ["10.0.0.0/8 via 10.0.2.2"]),
    "bastion-locked": ("locked", f"{MGMT}.12", [("eth1", "10.0.3.1/30")], ["10.0.0.0/8 via 10.0.3.2"]),
    "srv-sftp": ("", None, [("eth1", "10.2.1.10/24")], ["default via 10.2.1.1"]),
    "srv-web": ("web", None, [("eth1", "10.2.2.10/24")], ["default via 10.2.2.1"]),
    "srv-kbd": ("kbd", None, [("eth1", "10.2.3.10/24")], ["default via 10.2.3.1"]),
    "srv-legacy": ("legacy", None, [("eth1", "10.2.4.10/24")], ["default via 10.2.4.1"]),
}

# Fabric: name -> (loopback, [(iface number, address, p2p)])
FABRIC = {
    "edge1": ("10.255.0.1", [(1, "10.0.2.2/30", False), (2, "10.1.0.0/31", True), (3, "10.1.0.2/31", True)]),
    "edge2": ("10.255.0.2", [(1, "10.0.3.2/30", False), (2, "10.1.0.4/31", True), (3, "10.1.0.6/31", True)]),
    "spine1": ("10.255.0.11", [(1, "10.1.0.1/31", True), (2, "10.1.0.5/31", True)] + [(2 + i, f"10.1.1.{2 * (i - 1)}/31", True) for i in range(1, 5)]),
    "spine2": ("10.255.0.12", [(1, "10.1.0.3/31", True), (2, "10.1.0.7/31", True)] + [(2 + i, f"10.1.2.{2 * (i - 1)}/31", True) for i in range(1, 5)]),
}
ACCESS = {1: "10.2.1.1/24", 2: "10.2.2.1/24", 3: "10.2.3.1/24", 4: "10.2.4.1/24"}
for i in range(1, 5):
    ifs = [(1, f"10.1.1.{2 * (i - 1) + 1}/31", True), (2, f"10.1.2.{2 * (i - 1) + 1}/31", True), (3, ACCESS[i], False)]
    if i == 4:
        ifs.append((4, "10.2.5.1/24", False))
    FABRIC[f"leaf{i}"] = (f"10.255.0.2{i}", ifs)

LINKS = [
    ("bastion1:eth1", "bastion2:eth1"),
    ("bastion2:eth2", "edge1:eth1"),
    ("bastion-locked:eth1", "edge2:eth1"),
    ("edge1:eth2", "spine1:eth1"), ("edge1:eth3", "spine2:eth1"),
    ("edge2:eth2", "spine1:eth2"), ("edge2:eth3", "spine2:eth2"),
] + [(f"spine1:eth{2 + i}", f"leaf{i}:eth1") for i in range(1, 5)] + [(f"spine2:eth{2 + i}", f"leaf{i}:eth2") for i in range(1, 5)] + [
    ("leaf1:eth3", "srv-sftp:eth1"), ("leaf2:eth3", "srv-web:eth1"), ("leaf3:eth3", "srv-kbd:eth1"),
    ("leaf4:eth3", "srv-legacy:eth1"), ("leaf4:eth4", "ios1:eth1"),
]

# Static routes for the Linux fabric (the cEOS fabric runs OSPF instead).
# Everything goes over spine1; spine2 links are there but unused.
LEAF_NETS = {1: ["10.2.1.0/24"], 2: ["10.2.2.0/24"], 3: ["10.2.3.0/24"], 4: ["10.2.4.0/24", "10.2.5.0/24"]}
LINUX_FABRIC_ROUTES = {
    "edge1": ["10.0.0.0/8 via 10.1.0.1", "10.255.0.12/32 via 10.1.0.3"],
    "edge2": ["10.0.0.0/8 via 10.1.0.5", "10.255.0.12/32 via 10.1.0.7"],
    "spine1": ["10.0.2.0/30 via 10.1.0.0", "10.255.0.1/32 via 10.1.0.0", "10.0.3.0/30 via 10.1.0.4", "10.255.0.2/32 via 10.1.0.4"]
    + [f"{n} via 10.1.1.{2 * (i - 1) + 1}" for i in range(1, 5) for n in LEAF_NETS[i] + [f"10.255.0.2{i}/32"]],
    "spine2": ["10.0.0.0/8 via 10.1.0.2"],
}
for i in range(1, 5):
    LINUX_FABRIC_ROUTES[f"leaf{i}"] = [f"10.0.0.0/8 via 10.1.1.{2 * (i - 1)}"]

HEADER = """# YACM test lab ({what}). Written by topology.py: change that, not this file.
#
#   host --mgmt-- bastion1 -- bastion2 -- edge1 --+-- spine1 --+-- leaf1 -- srv-sftp
#   host --mgmt-- bastion-locked -------- edge2 --+-- spine2 --+-- leaf2 -- srv-web
#                                                              +-- leaf3 -- srv-kbd
#                                                              +-- leaf4 -- srv-legacy, ios1
#
# Only the bastions are reachable from this computer; everything else is
# reached through them, as in a real network. Logins: lab / lab (ios1, the
# YACM demo device: admin / cisco123).
"""


def linux_node(name, roles, mgmt, addrs, routes):
    lines = [f"    {name}:", "      kind: linux", "      image: yacm-lab-linux:latest"]
    if mgmt:
        lines.append(f"      mgmt-ipv4: {mgmt}")
    else:
        lines.append("      network-mode: none")
    if roles:
        lines.append(f"      env: {{ ROLES: \"{roles}\" }}")
    lines.append("      binds: [\".work/id_ed25519.pub:/etc/yacm/authorized_keys:ro\"]")
    ex = [f"ip link set dev {i} mtu {MTU}" for i, _ in addrs] + [f"ip addr add {a} dev {i}" for i, a in addrs]
    ex += [f"ip route replace {r}" for r in routes]
    lines.append("      exec:")
    lines += [f"        - {e}" for e in ex]
    return lines


def ios_node():
    return ["    ios1:", "      kind: linux", "      image: yacm-lab-linux:latest", "      network-mode: none",
            "      binds: [\".work/yacm-demo-device:/usr/local/bin/yacm-demo-device:ro\"]",
            "      cmd: /usr/local/bin/yacm-demo-device -ssh 0.0.0.0:22 -ssh-legacy \"\" -telnet 0.0.0.0:23 -hostname ios1 -flash /flash -hostkey /var/lib/yacm-demo/hostkey",
            "      exec:", f"        - ip link set dev eth1 mtu {MTU}", "        - ip addr add 10.2.5.10/24 dev eth1", "        - ip route replace default via 10.2.5.1"]


def write(path, what, fabric_lines):
    out = [HEADER.format(what=what), "name: yacm", "mgmt:", "  network: yacm-mgmt", f"  ipv4-subnet: {MGMT}.0/24", "topology:"]
    out += fabric_lines["kinds"]
    out.append("  nodes:")
    for name, (roles, mgmt, addrs, routes) in LINUX.items():
        out += linux_node(name, roles, mgmt, addrs, routes)
    out += ios_node()
    out += fabric_lines["nodes"]
    out.append("  links:")
    out += [f"    - endpoints: [\"{a}\", \"{b}\"]" for a, b in LINKS]
    with open(path, "w") as f:
        f.write("\n".join(out) + "\n")


ceos = {
    "kinds": ["  kinds:", "    arista_ceos:", "      image: ${CEOS_IMAGE:=ceos:4.36.3M}"],
    "nodes": [],
}
for name in FABRIC:
    ceos["nodes"] += [f"    {name}:", "      kind: arista_ceos", f"      startup-config: cfg/{name}.partial.cfg"]
write("yacm.clab.yml", "Arista cEOS fabric", ceos)

lin = {"kinds": [], "nodes": []}
for name, (lo, ifs) in FABRIC.items():
    lin["nodes"] += [f"    {name}:", "      kind: linux", "      image: yacm-lab-linux:latest", "      network-mode: none",
                     "      env: { ROLES: \"router\" }",
                     "      sysctls: { net.ipv4.ip_forward: 1 }",
                     "      exec:", f"        - ip addr add {lo}/32 dev lo"]
    lin["nodes"] += [f"        - ip link set dev eth{n} mtu {MTU}" for n, _, _ in ifs]
    lin["nodes"] += [f"        - ip addr add {a} dev eth{n}" for n, a, _ in ifs]
    lin["nodes"] += [f"        - ip route replace {r}" for r in LINUX_FABRIC_ROUTES[name]]
write("yacm-linux.clab.yml", "Linux routers instead of cEOS", lin)
print("wrote yacm.clab.yml and yacm-linux.clab.yml")
