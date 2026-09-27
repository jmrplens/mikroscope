# mikroscope {{MIKROSCOPE_VERSION}}: install script for RouterOS 7.24 or later. Container name: mikroscope
# Every object it creates carries the comment "mikroscope:mikroscope (managed by mikroscope)", which is how
# `mikroscope status` and `uninstall` recognize them later. It lists them in
# usb1/mikroscope/mikroscope.manifest.txt on the router, which `mikroscope uninstall` reads and deletes last.
#
# BEFORE RUNNING: put the agent image tar on the device as usb1/mikroscope.tar
# (upload it over WinBox/WebFig Files, or /tool/fetch it), or regenerate this
# script with --remote-image so the router pulls the image instead.
#
# It runs as one block: a check that fails stops it before anything is written,
# and a step that fails stops the steps after it.

{
:if (!([/system/resource/get version] ~ "^(7[.](2[4-9]|[3-9][0-9]|[1-9][0-9][0-9])|([89]|[1-9][0-9]+)[.])")) do={ :error "mikroscope: needs RouterOS 7.24 or later" }
:if ([:len [/system/package/find name="container" disabled=no]] = 0) do={ :error "mikroscope: the container package is not installed" }
:local dm [:tostr [/system/device-mode/get container]]; :if ($dm != "yes" && $dm != "true") do={ :error "mikroscope: device-mode container is not enabled" }
:if ([:len [/interface/veth/find name="veth-mikroscope"]] > 0 && [:len [/interface/veth/find name="veth-mikroscope" comment="mikroscope:mikroscope (managed by mikroscope)"]] = 0) do={ :error "mikroscope: veth veth-mikroscope exists and is not mikroscope's" }
:if ([:len [/container/envs/find list="mikroscope-env"]] > 0 && [:len [/container/envs/find list="mikroscope-env" key="MIKROSCOPE_TAG" value="mikroscope:mikroscope (managed by mikroscope)"]] = 0) do={ :error "mikroscope: envlist mikroscope-env exists and is not mikroscope's" }
:if ([:len [/interface/list/find name="LAN"]] = 0) do={ :error "mikroscope: interface list LAN does not exist" }
:if ([:len [/disk/find slot="usb1"]] = 0) do={ :error "mikroscope: disk usb1 does not exist" }
:if ([:len [/file/find name="usb1/mikroscope.tar"]] = 0) do={ :error "mikroscope: upload usb1/mikroscope.tar first" }
:if ([:len [/file/find name="usb1/mikroscope/mikroscope.manifest.txt"]] > 0) do={ :if (!([:typeof [:find [/file/get [find name="usb1/mikroscope/mikroscope.manifest.txt"] contents] "\ntag=mikroscope:mikroscope (managed by mikroscope)\n"]] = "num")) do={ :error "mikroscope: usb1/mikroscope/mikroscope.manifest.txt exists and is not this install's manifest" } }
# install manifest usb1/mikroscope/mikroscope.manifest.txt
:local m "mikroscope-manifest=1\nname=mikroscope\ntag=mikroscope:mikroscope (managed by mikroscope)\ndisk=usb1\nveth=veth-mikroscope\nsubnet=172.30.10.0/30\nport=9123\niface-list=LAN\naddr-list=LANs\nexpose=\ncontainer-name=\nremote-image=\ntoken=no\ndir=usb1/mikroscope\nfile=usb1/mikroscope/mikroscope.manifest.txt\nobject=/interface/veth name=veth-mikroscope\nobject=/ip/address interface=veth-mikroscope\nobject=/interface/list/member interface=veth-mikroscope list=LAN\nobject=/ip/firewall/address-list list=LANs address=172.30.10.0/30\nfile=usb1/mikroscope.tar\nobject=/container/envs list=mikroscope-env\nobject=/container interface=veth-mikroscope\ndir=usb1/mikroscope/mikroscope\n"; :if ([:len [/file/find name="usb1/mikroscope/mikroscope.manifest.txt"]] > 0) do={ /file/set [find name="usb1/mikroscope/mikroscope.manifest.txt"] contents=$m } else={ /file/add name="usb1/mikroscope/mikroscope.manifest.txt" contents=$m }
# veth interface veth-mikroscope
/interface/veth/add name="veth-mikroscope" address=172.30.10.2/30 gateway=172.30.10.1 comment="mikroscope:mikroscope (managed by mikroscope)"
# router address 172.30.10.1
/ip/address/add address=172.30.10.1/30 interface="veth-mikroscope" comment="mikroscope:mikroscope (managed by mikroscope)"
# interface-list membership LAN
/interface/list/member/add list="LAN" interface="veth-mikroscope" comment="mikroscope:mikroscope (managed by mikroscope)"
# address-list membership LANs
/ip/firewall/address-list/add list="LANs" address=172.30.10.0/30 comment="mikroscope:mikroscope (managed by mikroscope)"
# container mikroscope
:if ([:len [/container/envs/find list="mikroscope-env" key="MIKROSCOPE_TAG" value="mikroscope:mikroscope (managed by mikroscope)"]] > 0) do={ /container/envs/remove [find list="mikroscope-env"] }; /container/envs/add list="mikroscope-env" key=MIKROSCOPE_TAG value="mikroscope:mikroscope (managed by mikroscope)"; /container/envs/add list="mikroscope-env" key=RATE_HZ value="10"; /container/envs/add list="mikroscope-env" key=BUFFER_S value="60"; /container/envs/add list="mikroscope-env" key=PORT value="9123"; /container/envs/add list="mikroscope-env" key=ADDR value="172.30.10.2"; /container/envs/add list="mikroscope-env" key=MEM_LIMIT_MB value="16"; /container/envs/add list="mikroscope-env" key=CAPTURE_MB value="4"; /container/add file=usb1/mikroscope.tar interface="veth-mikroscope" root-dir=usb1/mikroscope/mikroscope envlist="mikroscope-env" logging=yes start-on-boot=yes restart-policy=on-failure restart-max-count=5 restart-interval=10s memory-max=64M privileged=yes ignore-remote-image-change=yes comment="mikroscope:mikroscope (managed by mikroscope)"; :local w 0; :while ([:len [/container/find comment="mikroscope:mikroscope (managed by mikroscope)" stopped]] = 0 && $w < 120) do={ :delay 1s; :set w ($w + 1) }; :if ([:len [/container/find comment="mikroscope:mikroscope (managed by mikroscope)" stopped]] = 0) do={ :error "mikroscope: the image was not extracted within 120 s; usb1/mikroscope.tar stays" }; /file/remove [find name="usb1/mikroscope.tar"]; /container/start [find comment="mikroscope:mikroscope (managed by mikroscope)"]
:local k 0; :while ([:len [/container/find comment="mikroscope:mikroscope (managed by mikroscope)" running]] = 0 && $k < 120) do={ :delay 1s; :set k ($k + 1) }
:if ([:len [/container/find comment="mikroscope:mikroscope (managed by mikroscope)" running]] > 0) do={ :put "mikroscope: agent running, http://172.30.10.2:9123/healthz" } else={ :put "mikroscope: not running yet; see /log/print where topics~\"container\"" }
}
# When it is done: /container/print where comment="mikroscope:mikroscope (managed by mikroscope)"
# The agent answers on http://172.30.10.2:9123/healthz from the router's LAN.
