# Linux node of the YACM test lab: jump hosts and end points (SSH, SFTP, an
# internal web server). What a node does comes from ROLES (see entrypoint.sh).
FROM ubuntu:24.04
RUN apt-get update \
 && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
      openssh-server openssh-sftp-server iproute2 iputils-ping curl netcat-openbsd \
      busybox ca-certificates procps \
 && rm -rf /var/lib/apt/lists/* \
 && useradd -m -s /bin/bash lab && echo 'lab:lab' | chpasswd \
 && mkdir -p /run/sshd /srv/www \
 && rm -f /etc/ssh/ssh_host_*
COPY entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod 755 /usr/local/bin/entrypoint.sh
CMD ["/usr/local/bin/entrypoint.sh"]
