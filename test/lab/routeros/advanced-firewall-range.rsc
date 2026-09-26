# advanced-firewall-range: the same raw rules with the LAN range written as src-address=!192.168.88.0/24, which no membership fixes
#
# advanced-firewall.rsc with the guide's own form of its first rule:
# in-interface-list=LAN src-address=!192.168.88.0/24, as MikroTik's "Building
# Advanced Firewall" writes it. The range is in the rule, not in a list, so
# no list an install joins lets the agent's /30 through: a veth in LAN is
# dropped by the first rule, a veth in no list by the last. The agent answers
# only once the rule itself changes.
#
# Apply with `test/lab/lab.sh profile advanced-firewall-range`. Idempotent: the
# lists are kept when they exist, and the raw rules of this profile and of
# advanced-firewall are replaced, so the two never stack. What this profile
# adds carries a comment that starts with "lab: advanced-firewall-range".
:if ([:len [/interface/list/find name="LAN"]] = 0) do={ /interface/list/add name=LAN comment="lab: advanced-firewall-range" }
:if ([:len [/interface/list/find name="WAN"]] = 0) do={ /interface/list/add name=WAN comment="lab: advanced-firewall-range" }
:if ([:len [/interface/list/member/find list="LAN" interface="ether2"]] = 0) do={ /interface/list/member/add list=LAN interface=ether2 comment="lab: advanced-firewall-range" }
:if ([:len [/interface/list/member/find list="WAN" interface="ether1"]] = 0) do={ /interface/list/member/add list=WAN interface=ether1 comment="lab: advanced-firewall-range" }
/ip/firewall/raw/remove [find comment~"^lab: advanced-firewall"]
/ip/firewall/raw/add chain=prerouting action=drop in-interface-list=LAN src-address=!192.168.88.0/24 comment="lab: advanced-firewall-range: drop local if not from default IP range"
/ip/firewall/raw/add chain=prerouting action=accept in-interface-list=LAN comment="lab: advanced-firewall-range: accept everything else from LAN"
/ip/firewall/raw/add chain=prerouting action=accept in-interface-list=WAN comment="lab: advanced-firewall-range: accept everything else from WAN"
/ip/firewall/raw/add chain=prerouting action=accept src-address-type=local comment="lab: advanced-firewall-range: accept local traffic between router interfaces"
/ip/firewall/raw/add chain=prerouting action=drop comment="lab: advanced-firewall-range: drop the rest"
