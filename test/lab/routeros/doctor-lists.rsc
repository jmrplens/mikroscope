# doctor-lists: the interface list LAN and the address list LANs that mikroscope 1.3.1's doctor asks for
#
# What `mikroscope doctor` 1.3.1 asks of a stock CHR before it lets install
# proceed, applied the way its own fixes say: an interface list named LAN (the
# veth joins it) and an address list named LANs with at least one entry (the
# /30 joins it). CHR ships neither, and doctor refuses an empty LANs even on a
# router with no rule that reads it. Measured on the lab, CHR 7.24.4,
# 2026-09-26.
#
# Apply with `test/lab/lab.sh profile doctor-lists`. Idempotent: a list or
# entry that exists already, from this profile or another, is kept as it is.
# What this profile adds carries the comment "lab: doctor-lists".
:if ([:len [/interface/list/find name="LAN"]] = 0) do={ /interface/list/add name=LAN comment="lab: doctor-lists" }
:if ([:len [/interface/list/member/find list="LAN" interface="ether2"]] = 0) do={ /interface/list/member/add list=LAN interface=ether2 comment="lab: doctor-lists" }
:if ([:len [/ip/firewall/address-list/find list="LANs" address="192.168.88.0/24"]] = 0) do={ /ip/firewall/address-list/add list=LANs address=192.168.88.0/24 comment="lab: doctor-lists" }
