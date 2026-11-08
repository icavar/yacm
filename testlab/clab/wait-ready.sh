#!/bin/bash
# Waits until the YACM lab is usable: sshd on the bastions, and every end
# point reachable from bastion2 (routing converged). Up to 5 minutes.
set -u
fabric=${1:-ceos}
deadline=$((SECONDS + 300))
say() { printf '%s %s\n' "$(date +%T)" "$*"; }
until_ok() {
	local what=$1; shift
	while ! "$@" >/dev/null 2>&1; do
		if ((SECONDS > deadline)); then
			say "timeout: $what"
			return 1
		fi
		sleep 3
	done
	say "ready: $what"
}
until_ok "bastion1 sshd" nc -z -w 2 172.30.0.11 22 || exit 1
until_ok "bastion-locked sshd" nc -z -w 2 172.30.0.12 22 || exit 1
if [ "$fabric" = ceos ]; then
	for n in edge1 edge2 spine1 spine2 leaf1 leaf2 leaf3 leaf4; do
		until_ok "$n EOS" docker exec clab-yacm-$n Cli -p 15 -c "show version" || exit 1
	done
fi
# Routing: from bastion2 to every end point and loopback, and back.
for ip in 10.255.0.1 10.255.0.21 10.255.0.24 10.2.1.10 10.2.2.10 10.2.3.10 10.2.4.10 10.2.5.10; do
	until_ok "route to $ip" docker exec clab-yacm-bastion2 ping -c1 -W1 "$ip" || exit 1
done
until_ok "route from bastion-locked to leaf3" docker exec clab-yacm-bastion-locked ping -c1 -W1 10.255.0.23 || exit 1
for ip in 10.255.0.21 10.255.0.24; do
	until_ok "sshd on $ip" docker exec clab-yacm-bastion2 nc -z -w 2 "$ip" 22 || exit 1
done
until_ok "ios1 (demo device) sshd" docker exec clab-yacm-bastion2 nc -z -w 2 10.2.5.10 22 || exit 1
say "lab ready"
