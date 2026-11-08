# YACM test lab

End-to-end tests of YACM against a small network in containers: jump host
chains, an Arista cEOS fabric, Linux servers and the YACM demo device. One
command builds the lab, runs the tests and removes the lab again.

```
host --mgmt-- bastion1 -- bastion2 -- edge1 --+-- spine1 --+-- leaf1 -- srv-sftp
host --mgmt-- bastion-locked -------- edge2 --+-- spine2 --+-- leaf2 -- srv-web
                                                           +-- leaf3 -- srv-kbd
                                                           +-- leaf4 -- srv-legacy, ios1
```

Only the two bastions are reachable from the host (172.30.0.11 and .12).
Everything else is reached through them, as in a real network: the cEOS
nodes refuse SSH and telnet from outside 10.0.0.0/8.

| Node | What it is | Used for |
| --- | --- | --- |
| bastion1, bastion2 | OpenSSH, `GatewayPorts clientspecified` | two-hop jump chain, file server tunnel |
| bastion-locked | OpenSSH, `GatewayPorts no`, modern algorithms only | YACM explains why a file server tunnel cannot work |
| edge1, edge2, spine1, spine2, leaf1–4 | Arista cEOS 4.36.3M, OSPF, telnet on edge1 | sessions to network devices, telnet through jump hosts, SFTP/SCP, show tech, `copy http:` / `copy tftp:` from a device |
| srv-sftp | OpenSSH with SFTP | file browser, large transfers, tunnels (-L, -D, -R) |
| srv-web | internal web server on port 80 | reached only through tunnels and the SOCKS/HTTP proxy |
| srv-kbd | keyboard-interactive only | keyboard-interactive login |
| srv-legacy | old algorithms only (group1/14-sha1, ssh-rsa, CBC) | algorithm profiles |
| ios1 | the YACM demo device (IOS-like, SCP only) | Cisco path: `dir`, `verify /md5`, SCP |

Logins: `lab` / `lab` everywhere, `admin` / `cisco123` on ios1.

## Running it

Once on the laptop: Docker Engine, Containerlab and the cEOS image
(`docker import cEOS64-lab-4.36.3M.tar ceos:4.36.3M`). Then, in the
repository:

```sh
make -C testlab/clab e2e                  # build, start, test, stop
make -C testlab/clab e2e TESTS=TestLabTunnels   # only some tests
make -C testlab/clab e2e FABRIC=linux     # Linux routers instead of cEOS
```

The whole run takes a few minutes. The lab needs about 10 GB of RAM while
it runs and nothing after `down`. Test output is in
`testlab/clab/.work/e2e.log`, container logs in `testlab/clab/.work/logs/`.

To click through the lab in YACM:

```sh
make -C testlab/clab up          # start it and wait until it is ready
make -C testlab/clab lab-file    # write testlab/clab/testlab.yacm
# YACM: import testlab.yacm (export password lab-export-pw)
make -C testlab/clab down
```

`testlab.yacm` has a group per topic: jump hosts, the fabric (through
bastion1 → bastion2), servers (srv-sftp has an -L tunnel to srv-web on
127.0.0.1:18080 and a SOCKS/HTTP proxy on 127.0.0.1:11080; tunnels from a
file arrive switched off, start them in Tunnels), sessions behind the locked
bastion, edge1 over telnet and a group that connects through the SOCKS proxy.

The topologies are written by `testlab/clab/topology.py` (one source for the
cEOS and the Linux variant); change that and run it, not the YAML files.

## Automatic runs

The Lab workflow (`.github/workflows/lab.yml`):

- **linux-fabric** on a GitHub runner, with Linux routers instead of cEOS: on
  every push to `main` that changes more than documentation.
- **ceos-fabric** on the maintainer's computer through a self-hosted runner
  with the label `clab`: at night (01:23 UTC) only when `main` changed in
  the last day, and on demand before a release (Actions > Lab > Run
  workflow). When the computer is off, GitHub keeps the job waiting for up
  to a day.

It never runs for pull requests, and never in a public repository: the
self-hosted runner is registered on a **private** repository only, whose
Lab workflow tests the public `main` branch (repository variable
`YACM_LAB_SOURCE`, for example `icavar/yacm`). So only code that has been
merged reaches the computer, and nobody can send it code with a pull
request.

### Registering a computer as a runner

1. Tools the runner uses:
   `sudo apt install make git openssh-client netcat-openbsd`
2. A user of its own (the `docker` group is as good as root, so not your own
   account):
   ```sh
   sudo adduser --disabled-password --gecos "" gh-runner
   sudo usermod -aG docker,clab_admins gh-runner
   ```
3. On GitHub, in the **private** repository: Settings > Actions > Runners >
   New self-hosted runner > Linux, x64. Keep the page open; it shows the
   download commands and a one-time token. Enter the token only on that
   computer.
4. As the runner user, run the download commands from that page, then:
   ```sh
   sudo -iu gh-runner
   mkdir actions-runner && cd actions-runner
   # … download and extract, as shown on the GitHub page …
   ./config.sh --url https://github.com/<owner>/<private-repo> --token <token from the page> \
       --name lab-runner --labels clab --unattended
   exit
   ```
5. Start it as a service, so it runs after every reboot (your own account
   cannot enter the runner's home, so as root):
   ```sh
   sudo bash -c 'cd /home/gh-runner/actions-runner && ./svc.sh install gh-runner && ./svc.sh start'
   systemctl list-units 'actions.runner.*'   # the service runs as gh-runner
   ```
6. On GitHub, in the private repository: Settings > Secrets and variables >
   Actions > Variables > New repository variable: Name `YACM_CLAB_RUNNER`,
   Value `true` (only the word), and `YACM_LAB_SOURCE` with the public
   repository (`icavar/yacm`).
7. Actions > Lab > Run workflow > fabric `ceos`.

The cEOS image is shared through Docker, so the runner user sees it without
importing it again.

## Troubleshooting

- `wait-ready.sh` waits up to 5 minutes for sshd, EOS and the routes. cEOS
  usually needs one to two minutes; `docker logs clab-yacm-leaf1` shows its
  boot.
- Many cEOS at once need higher inotify limits (`/etc/sysctl.d/90-clab.conf`
  from the setup).
- A lab left over from an interrupted run: `make -C testlab/clab down`.
- Every link runs at MTU 1500 (`topology.py` sets it on the Linux nodes).
  Containerlab's default of 9500 against cEOS's 1500 makes large SSH packets
  vanish: transfers stop after the first window, old key exchanges fail.
