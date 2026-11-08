#!/bin/bash
# Starts sshd (and the extra services of the node) according to ROLES, a
# comma separated list:
#   gateway   GatewayPorts clientspecified: remote tunnels may listen on the
#             jump host's own addresses (file servers reached through it)
#   locked    GatewayPorts no and modern algorithms only
#   legacy    old algorithms only (diffie-hellman-group14/group1-sha1, ssh-rsa,
#             CBC ciphers, hmac-sha1), like an old IOS
#   kbd       keyboard-interactive only, no plain password authentication
#   web       an internal web server on port 80 (reached through tunnels)
# Login lab / lab; keys from /etc/yacm/authorized_keys when it is mounted.
set -e
mkdir -p /run/sshd
[ -e /etc/ssh/ssh_host_ed25519_key ] || ssh-keygen -A >/dev/null
if [ -s /etc/yacm/authorized_keys ]; then
	install -d -o lab -g lab -m 700 /home/lab/.ssh
	install -o lab -g lab -m 600 /etc/yacm/authorized_keys /home/lab/.ssh/authorized_keys
fi

cfg=/etc/ssh/sshd_config.d/10-yacm-lab.conf
{
	echo "PasswordAuthentication yes"
	echo "KbdInteractiveAuthentication yes"
	echo "AllowTcpForwarding yes"
	echo "UseDNS no"
	echo "MaxSessions 64"
	echo "MaxStartups 64:30:128"
} >"$cfg"

has() { case ",${ROLES}," in *",$1,"*) return 0 ;; esac; return 1; }

if has gateway; then
	echo "GatewayPorts clientspecified" >>"$cfg"
fi
if has locked; then
	{
		echo "GatewayPorts no"
		echo "KexAlgorithms sntrup761x25519-sha512@openssh.com,curve25519-sha256"
		echo "Ciphers chacha20-poly1305@openssh.com,aes256-gcm@openssh.com"
		echo "MACs hmac-sha2-256-etm@openssh.com,hmac-sha2-512-etm@openssh.com"
	} >>"$cfg"
fi
if has legacy; then
	{
		echo "KexAlgorithms diffie-hellman-group14-sha1,diffie-hellman-group1-sha1"
		echo "HostKeyAlgorithms ssh-rsa"
		echo "PubkeyAcceptedAlgorithms +ssh-rsa"
		echo "Ciphers aes128-cbc,aes256-cbc,3des-cbc"
		echo "MACs hmac-sha1"
	} >>"$cfg"
fi
if has kbd; then
	# Keep this before the distribution's own settings (first value wins).
	sed -i 's/^PasswordAuthentication yes/PasswordAuthentication no/' "$cfg"
fi

/usr/sbin/sshd -t -f /etc/ssh/sshd_config

if has web; then
	echo "internal web server of $(hostname): reached through a YACM tunnel" >/srv/www/index.html
	head -c 1048576 /dev/urandom >/srv/www/1mb.bin
	busybox httpd -p 80 -h /srv/www
fi

exec /usr/sbin/sshd -D -e
