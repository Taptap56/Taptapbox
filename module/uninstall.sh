#!/system/bin/sh
# taptapbox uninstall cleanup: stop processes, flush iptables rules,
# and remove ALL module data at /data/adb/taptapbox.
MODDIR=${0%/*}
DATA=/data/adb/taptapbox

# NOTE: "bin/..." patterns avoid matching this script's own path.
pkill -f "/data/adb/taptapbox/bin/taptapbox" 2>/dev/null
# bpf2socks sisa versi lama (eBPF sudah dihapus): matikan bila masih ada.
pkill -9 -f "bpf2socks" 2>/dev/null
sleep 1

if command -v iptables >/dev/null 2>&1; then IPT4=iptables; else IPT4=/system/bin/iptables; fi
if command -v ip6tables >/dev/null 2>&1; then IPT6=ip6tables; else IPT6=/system/bin/ip6tables; fi
if command -v ip >/dev/null 2>&1; then IP=ip; else IP=/system/bin/ip; fi

for IPT in $IPT4 $IPT6; do
  # redirect chains (nat)
  for CH in OUTPUT PREROUTING; do
    while $IPT -t nat -D $CH -p tcp -j TAPTAPBOX 2>/dev/null; do :; done
    while $IPT -t nat -D $CH -p udp -j TAPTAPBOX 2>/dev/null; do :; done
  done
  $IPT -t nat -F TAPTAPBOX 2>/dev/null
  $IPT -t nat -X TAPTAPBOX 2>/dev/null
  # QUIC block chain (filter)
  while $IPT -t filter -D OUTPUT -j TAPTAPBOX_F 2>/dev/null; do :; done
  $IPT -t filter -F TAPTAPBOX_F 2>/dev/null
  $IPT -t filter -X TAPTAPBOX_F 2>/dev/null
  # tproxy chains (mangle)
  while $IPT -t mangle -D PREROUTING -j TAPTAPBOX_TP 2>/dev/null; do :; done
  while $IPT -t mangle -D OUTPUT -j TAPTAPBOX_TM 2>/dev/null; do :; done
  $IPT -t mangle -F TAPTAPBOX_TP 2>/dev/null
  $IPT -t mangle -X TAPTAPBOX_TP 2>/dev/null
  $IPT -t mangle -F TAPTAPBOX_TM 2>/dev/null
  $IPT -t mangle -X TAPTAPBOX_TM 2>/dev/null
  # MSS clamp (mangle)
  while $IPT -t mangle -D OUTPUT -p tcp --tcp-flags SYN,RST SYN -j TCPMSS --clamp-mss-to-pmtu 2>/dev/null; do :; done
done

# Blokir IPv6 anti-bocor (filter) — REJECT agar aplikasi langsung fallback ke IPv4
while $IPT6 -D OUTPUT -o lo -j ACCEPT 2>/dev/null; do :; done
while $IPT6 -D OUTPUT -m owner ! --uid-owner 0 -j REJECT 2>/dev/null; do :; done
while $IPT6 -D FORWARD -j REJECT 2>/dev/null; do :; done
# legacy: sisa rule DROP dari build lama
while $IPT6 -D OUTPUT -m owner ! --uid-owner 0 -j DROP 2>/dev/null; do :; done
while $IPT6 -D FORWARD -j DROP 2>/dev/null; do :; done
# Blok QUIC via IPv6 (filter)
while $IPT6 -D OUTPUT -m owner --uid-owner 0 -j RETURN 2>/dev/null; do :; done
while $IPT6 -D OUTPUT -p udp --dport 443 -j REJECT 2>/dev/null; do :; done

# DNS hijack chain (nat IPv4)
while $IPT4 -t nat -D OUTPUT -p udp --dport 53 -j TAPTAPBOX_DNS 2>/dev/null; do :; done
while $IPT4 -t nat -D PREROUTING -p udp --dport 53 -j TAPTAPBOX_DNS 2>/dev/null; do :; done
$IPT4 -t nat -F TAPTAPBOX_DNS 2>/dev/null
$IPT4 -t nat -X TAPTAPBOX_DNS 2>/dev/null

# TUN interface (mode TUN ala TaptapClash)
$IP link del tun0 2>/dev/null

# tproxy policy routing (v1.0.7: fwmark bit 24 -> table 2024 pref 100)
$IP rule del fwmark 0x1000000/0x1000000 table 2024 pref 100 2>/dev/null
$IP route del local 0.0.0.0/0 dev lo table 2024 2>/dev/null
# nilai lama (v1.0.6 ke bawah)
$IP rule del fwmark 0x1/0x1 table 100 2>/dev/null
$IP route del local 0.0.0.0/0 dev lo table 100 2>/dev/null

# hotspot (rule dari fitur Hotspot)
for P in 5000 5010 5020 5030 5040 5050 6000; do
  while $IP rule del pref $P 2>/dev/null; do :; done
done
for NET in 10.0.0.0/8 172.16.0.0/12 192.168.0.0/16; do
  while $IPT4 -D FORWARD -s $NET -o tun0 -j ACCEPT 2>/dev/null; do :; done
  while $IPT4 -t nat -D PREROUTING ! -i tun0 -s $NET -p udp --dport 53 -j DNAT --to 1.1.1.1 2>/dev/null; do :; done
done
while $IPT4 -D FORWARD -i tun0 -j ACCEPT 2>/dev/null; do :; done
while $IPT6 -D FORWARD -j REJECT --reject-with icmp6-no-route 2>/dev/null; do :; done
umount /proc/sys/net/ipv4/ip_forward 2>/dev/null

# hapus SEMUA data modul (server, pengaturan, log, webui, backup)
rm -rf "$DATA"
# salinan helper updater (v1.0.35) di luar data dir — jangan jadi yatim 9MB
rm -f /data/local/tmp/taptapbox-fetch
exit 0
