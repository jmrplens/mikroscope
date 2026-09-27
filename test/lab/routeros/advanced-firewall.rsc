# advanced-firewall: the raw rules of MikroTik's "Building Advanced Firewall", with the LAN range as the address list LANs
#
# The two raw rules of MikroTik's "Building Advanced Firewall" guide
# (help.mikrotik.com, RouterOS › Firewall and QoS) that decide whether an
# agent's replies leave the router, with the guide's LAN range written as an
# address list, which is the form an install's list membership fixes:
#
#   drop local if not from default IP range   in-interface-list=LAN and a
#       source outside LANs (the guide: src-address=!192.168.88.0/24). An
#       agent whose veth is in LAN but whose /30 is not in LANs is dropped here.
#   accept everything else from LAN, accept everything else from WAN, accept
#       local traffic between router interfaces, drop the rest: together, an
#       in-interface-list=!LAN drop that lets WAN through. An agent whose veth
#       is in no list is dropped by the last rule.
#
# WAN is accepted as in the guide, so the router's own traffic on ether1 (the
# pull from Docker Hub, the DHCP client) still gets its replies. The guide's
# bogon, TCP-flag and ICMP rules decide nothing about the agent and are left
# out.
#
# Apply with `make lab-profile PROFILE=advanced-firewall`. Idempotent: the
# lists are kept when they exist, and the raw rules of this profile and of
# advanced-firewall-range are replaced, so the two never stack. What this
# profile adds carries a comment that starts with "lab: advanced-firewall".
:if ([:len [/interface/list/find name="LAN"]] = 0) do={ /interface/list/add name=LAN comment="lab: advanced-firewall" }
:if ([:len [/interface/list/find name="WAN"]] = 0) do={ /interface/list/add name=WAN comment="lab: advanced-firewall" }
:if ([:len [/interface/list/member/find list="LAN" interface="ether2"]] = 0) do={ /interface/list/member/add list=LAN interface=ether2 comment="lab: advanced-firewall" }
:if ([:len [/interface/list/member/find list="WAN" interface="ether1"]] = 0) do={ /interface/list/member/add list=WAN interface=ether1 comment="lab: advanced-firewall" }
:if ([:len [/ip/firewall/address-list/find list="LANs" address="192.168.88.0/24"]] = 0) do={ /ip/firewall/address-list/add list=LANs address=192.168.88.0/24 comment="lab: advanced-firewall" }
/ip/firewall/raw/remove [find comment~"^lab: advanced-firewall"]
/ip/firewall/raw/add chain=prerouting action=drop in-interface-list=LAN src-address-list=!LANs comment="lab: advanced-firewall: drop local if not from LANs"
/ip/firewall/raw/add chain=prerouting action=accept in-interface-list=LAN comment="lab: advanced-firewall: accept everything else from LAN"
/ip/firewall/raw/add chain=prerouting action=accept in-interface-list=WAN comment="lab: advanced-firewall: accept everything else from WAN"
/ip/firewall/raw/add chain=prerouting action=accept src-address-type=local comment="lab: advanced-firewall: accept local traffic between router interfaces"
/ip/firewall/raw/add chain=prerouting action=drop comment="lab: advanced-firewall: drop the rest"
