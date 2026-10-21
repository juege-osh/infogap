#!/bin/sh
# Only this component's named bridge and chains are touched. No global flush.
set -eu
network=infogap-browser-net
bridge=ig-browser0
subnet=172.29.240.0/24
chain=IG_BROWSER_OUT
mode=${1:---check}
case "$mode" in --check|--apply) ;; *) exit 2 ;; esac
check_network() {
  actual=$(docker network inspect "$network" --format '{{.Driver}} {{.EnableIPv6}} {{index .Options "com.docker.network.bridge.name"}} {{(index .IPAM.Config 0).Subnet}}' 2>/dev/null) || return 1
  [ "$actual" = "bridge false $bridge $subnet" ]
}
if [ "$mode" = --apply ]; then
  if ! docker network inspect "$network" >/dev/null 2>&1; then
    docker network create --driver bridge --subnet "$subnet" --gateway 172.29.240.1 \
      --opt com.docker.network.bridge.name="$bridge" --opt com.docker.network.bridge.enable_icc=false "$network" >/dev/null
  fi
  check_network || { echo 'Browser network configuration mismatch' >&2; exit 1; }
  if ! iptables -w 5 -S "$chain" >/dev/null 2>&1; then
    iptables -w 5 -N "$chain"
    for destination in 43.242.200.72/32 0.0.0.0/8 10.0.0.0/8 100.64.0.0/10 127.0.0.0/8 169.254.0.0/16 172.16.0.0/12 192.0.0.0/24 192.168.0.0/16 198.18.0.0/15 224.0.0.0/4 240.0.0.0/4; do
      iptables -w 5 -A "$chain" -d "$destination" -j DROP
    done
    iptables -w 5 -A "$chain" -p tcp --dport 443 -j ACCEPT
    for resolver in 1.1.1.1 8.8.8.8; do
      iptables -w 5 -A "$chain" -d "$resolver" -p udp --dport 53 -j ACCEPT
      iptables -w 5 -A "$chain" -d "$resolver" -p tcp --dport 53 -j ACCEPT
    done
    iptables -w 5 -A "$chain" -j DROP
  fi
  # The server public address is upstream NAT; it must also be blocked before HTTPS.
  iptables -w 5 -C "$chain" -d 43.242.200.72/32 -j DROP 2>/dev/null || iptables -w 5 -I "$chain" 1 -d 43.242.200.72/32 -j DROP
  iptables -w 5 -C DOCKER-USER -i "$bridge" -j "$chain" 2>/dev/null || iptables -w 5 -I DOCKER-USER 1 -i "$bridge" -j "$chain"
  iptables -w 5 -C INPUT -i "$bridge" -j DROP 2>/dev/null || iptables -w 5 -I INPUT 1 -i "$bridge" -j DROP
fi
check_network || { echo 'Browser isolated network is unavailable' >&2; exit 1; }
iptables -w 5 -C DOCKER-USER -i "$bridge" -j "$chain"
iptables -w 5 -C INPUT -i "$bridge" -j DROP
for destination in 43.242.200.72/32 0.0.0.0/8 10.0.0.0/8 100.64.0.0/10 127.0.0.0/8 169.254.0.0/16 172.16.0.0/12 192.0.0.0/24 192.168.0.0/16 198.18.0.0/15 224.0.0.0/4 240.0.0.0/4; do
  iptables -w 5 -C "$chain" -d "$destination" -j DROP
done
iptables -w 5 -C "$chain" -p tcp --dport 443 -j ACCEPT
iptables -w 5 -C "$chain" -j DROP
printf '%s\n' 'Browser isolated network and private-address rules verified'
