# custom-lists: an interface list MYLAN with ether2 in it, and an address list MYNETS with the LAN range, for installs that name lists of their own
#
# The lists of a router whose firewall is written against names other than
# defconf's LAN and LANs: `install --iface-list MYLAN --addr-list MYNETS`
# joins these instead. MYNETS holds the LAN range, so the doctor of a release
# that asks for an address list with entries (1.3.1) passes here too.
#
# Apply with `test/lab/lab.sh profile custom-lists`. Idempotent: a list or
# entry that exists already, from this profile or another, is kept as it is.
# What this profile adds carries the comment "lab: custom-lists".
:if ([:len [/interface/list/find name="MYLAN"]] = 0) do={ /interface/list/add name=MYLAN comment="lab: custom-lists" }
:if ([:len [/interface/list/member/find list="MYLAN" interface="ether2"]] = 0) do={ /interface/list/member/add list=MYLAN interface=ether2 comment="lab: custom-lists" }
:if ([:len [/ip/firewall/address-list/find list="MYNETS" address="192.168.88.0/24"]] = 0) do={ /ip/firewall/address-list/add list=MYNETS address=192.168.88.0/24 comment="lab: custom-lists" }
