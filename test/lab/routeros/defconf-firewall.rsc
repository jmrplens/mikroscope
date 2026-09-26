# defconf-firewall: a stock home router's lists and filter rules (ether2 LAN, ether1 WAN)
#
# The interface lists and the IPv4 filter rules of RouterOS 7's default
# configuration on a home RouterBOARD, which CHR does not ship: LAN and WAN,
# input dropped unless it comes from LAN, forward from WAN dropped unless it
# was dst-natted. Nothing here reads which interface list the agent's veth is
# in or which address list its /30 is in: an agent's replies to a LAN client
# are established connections, so on a router configured like this the agent
# needs no list membership.
#
# The input rule drops anything that does not come in on a LAN interface, so
# after this profile the lab's way in over ether1 (`ssh lab-wan` inside the
# lab container) is closed; ether2, where every mikroscope-lab verb connects,
# stays open.
#
# Apply with `make lab-profile PROFILE=defconf-firewall`. Idempotent: the
# lists are kept when they exist (doctor-lists makes LAN too), and the rules
# are replaced. What this profile adds carries a comment that starts with
# "lab: defconf-firewall".
:if ([:len [/interface/list/find name="LAN"]] = 0) do={ /interface/list/add name=LAN comment="lab: defconf-firewall" }
:if ([:len [/interface/list/find name="WAN"]] = 0) do={ /interface/list/add name=WAN comment="lab: defconf-firewall" }
:if ([:len [/interface/list/member/find list="LAN" interface="ether2"]] = 0) do={ /interface/list/member/add list=LAN interface=ether2 comment="lab: defconf-firewall" }
:if ([:len [/interface/list/member/find list="WAN" interface="ether1"]] = 0) do={ /interface/list/member/add list=WAN interface=ether1 comment="lab: defconf-firewall" }
/ip/firewall/filter/remove [find comment~"^lab: defconf-firewall"]
/ip/firewall/filter/add chain=input action=accept connection-state=established,related,untracked comment="lab: defconf-firewall: accept established,related,untracked"
/ip/firewall/filter/add chain=input action=drop connection-state=invalid comment="lab: defconf-firewall: drop invalid"
/ip/firewall/filter/add chain=input action=accept protocol=icmp comment="lab: defconf-firewall: accept ICMP"
/ip/firewall/filter/add chain=input action=accept dst-address=127.0.0.1 comment="lab: defconf-firewall: accept to local loopback (for CAPsMAN)"
/ip/firewall/filter/add chain=input action=drop in-interface-list=!LAN comment="lab: defconf-firewall: drop all not coming from LAN"
/ip/firewall/filter/add chain=forward action=accept ipsec-policy=in,ipsec comment="lab: defconf-firewall: accept in ipsec policy"
/ip/firewall/filter/add chain=forward action=accept ipsec-policy=out,ipsec comment="lab: defconf-firewall: accept out ipsec policy"
/ip/firewall/filter/add chain=forward action=fasttrack-connection connection-state=established,related comment="lab: defconf-firewall: fasttrack"
/ip/firewall/filter/add chain=forward action=accept connection-state=established,related,untracked comment="lab: defconf-firewall: accept established,related, untracked"
/ip/firewall/filter/add chain=forward action=drop connection-state=invalid comment="lab: defconf-firewall: drop invalid"
/ip/firewall/filter/add chain=forward action=drop connection-state=new connection-nat-state=!dstnat in-interface-list=WAN comment="lab: defconf-firewall: drop all from WAN not DSTNATed"
