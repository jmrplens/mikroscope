# What `mikroscope doctor` 1.3.1 asks of a stock CHR before it lets install
# proceed, applied the way its own fixes say: an interface list named LAN (the
# veth joins it) and an address list named LANs with at least one entry (the
# /30 joins it). CHR ships neither, and doctor refuses an empty LANs even on a
# router with no rule that reads it. Measured on the lab, CHR 7.24.4,
# 2026-09-26. Apply with: test/lab/lab.sh import test/lab/routeros/doctor-lists.rsc
/interface/list/add name=LAN comment="lab: mikroscope doctor prerequisite"
/interface/list/member/add list=LAN interface=ether2 comment="lab: mikroscope doctor prerequisite"
/ip/firewall/address-list/add list=LANs address=192.168.88.0/24 comment="lab: mikroscope doctor prerequisite"
