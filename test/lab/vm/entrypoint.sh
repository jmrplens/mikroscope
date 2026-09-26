#!/bin/bash
# shellcheck disable=SC2054 # QEMU option values carry commas; they are not array separators.
# The lab container's PID 1: it wires the router's two NICs, publishes the
# router and the agent on the container's own ports, and runs QEMU until the
# guest powers off. The container IS the machine: the container exiting is the
# power going off, and `docker start` is the power coming back — which is the
# cold reboot RouterOS asks for to confirm `/system/device-mode/update`.
#
# Inside this network namespace:
#
#   ether1 (WAN)  QEMU user networking: DHCP 10.0.2.15, NAT to the internet
#                 through the container's eth0. Pulls from Docker Hub and
#                 fetches from MikroTik go this way. 127.0.0.1:10022 in here
#                 forwards to the router's SSH on this side: the first-boot
#                 path, before ether2 has an address, and a way back in when a
#                 test has broken the LAN side.
#   ether2 (LAN)  a tap, lan0, with this namespace as the one LAN host
#                 (LAB_LAN_HOST) and the router at LAB_LAN_ROUTER. The CLI
#                 under test runs here (lab.sh cli), so it reaches the router
#                 and the agent's veth exactly as a LAN host does.
#
# LAB_AGENT_ROUTES go to the router over lan0. They are what keeps an agent
# address such as the default 172.30.10.2 inside the lab: without them the
# namespace's default route leads to the host and on to whatever the host's
# gateway routes that address to — on the machine this lab was built on, a
# production router whose own agent answers on 172.30.10.2.
set -euo pipefail

: "${LAB_ARCH:?}" "${LAB_DISK:?}"
LAB_MEM=${LAB_MEM:-1024}
LAB_CPUS=${LAB_CPUS:-2}
LAB_LAN_ROUTER=${LAB_LAN_ROUTER:-192.168.88.1}
LAB_LAN_HOST=${LAB_LAN_HOST:-192.168.88.10/24}
LAB_AGENT_ROUTES=${LAB_AGENT_ROUTES:-172.30.0.0/16}
LAB_AGENT_TARGET=${LAB_AGENT_TARGET:-172.30.10.2:9123}
LAB_CONSOLE_LOG=${LAB_CONSOLE_LOG:-}
RUN=/run/lab
mkdir -p "$RUN"

log() { printf '%s lab: %s\n' "$(date -u +%H:%M:%S)" "$*"; }

/lab/vm/ssh-setup.sh

ip tuntap add dev lan0 mode tap
ip addr add "$LAB_LAN_HOST" dev lan0
ip link set lan0 up
for net in $LAB_AGENT_ROUTES; do
	# onlink: the tap has no carrier until QEMU opens it.
	ip route add "$net" via "$LAB_LAN_ROUTER" dev lan0 onlink
done

# The router's services and the agent, on this container's ports, for Docker
# to publish on the host's loopback. socat connects from lan0's address, so
# the router sees every one of them as a LAN client.
fwd() { socat "TCP-LISTEN:$1,fork,reuseaddr" "TCP:$2" & }
fwd 22 "$LAB_LAN_ROUTER:22"
fwd 80 "$LAB_LAN_ROUTER:80"
fwd 8728 "$LAB_LAN_ROUTER:8728"
fwd 9123 "$LAB_AGENT_TARGET"

console="socket,id=con,path=$RUN/console.sock,server=on,wait=off"
[ -n "$LAB_CONSOLE_LOG" ] && console+=",logfile=$LAB_CONSOLE_LOG,logappend=on"
common=(
	-name "mikroscope-lab-$LAB_ARCH"
	-smp "$LAB_CPUS" -m "$LAB_MEM"
	-display none
	-chardev "$console" -serial chardev:con
	-monitor "unix:$RUN/monitor.sock,server=on,wait=off"
	-netdev "user,id=wan,hostfwd=tcp:127.0.0.1:10022-:22"
	-device virtio-net-pci,netdev=wan,mac=52:54:00:4d:53:01
	-netdev tap,id=lan,ifname=lan0,script=no,downscript=no
	-device virtio-net-pci,netdev=lan,mac=52:54:00:4d:53:02
)

case "$LAB_ARCH" in
x86_64)
	accel=(-machine q35,accel=kvm -cpu host)
	if [ ! -w /dev/kvm ]; then
		log "no /dev/kvm: falling back to TCG, expect a boot many times slower"
		accel=(-machine q35,accel=tcg -cpu max)
	fi
	qemu=(qemu-system-x86_64 "${accel[@]}" "${common[@]}"
		-drive "file=$LAB_DISK,if=virtio,format=qcow2,cache=writeback")
	;;
arm64)
	# CHR arm64 boots through UEFI only. On an x86 host there is no KVM for
	# it, so this is TCG: correct, and slow.
	accel=(-machine virt -cpu max)
	if [ "$(uname -m)" = aarch64 ] && [ -w /dev/kvm ]; then
		accel=(-machine virt,accel=kvm -cpu host)
	fi
	qemu=(qemu-system-aarch64 "${accel[@]}" "${common[@]}"
		-bios /usr/share/qemu-efi-aarch64/QEMU_EFI.fd
		-drive "file=$LAB_DISK,if=none,id=hd0,format=qcow2,cache=writeback"
		-device virtio-blk-pci,drive=hd0)
	;;
*)
	log "LAB_ARCH must be x86_64 or arm64, got $LAB_ARCH"
	exit 2
	;;
esac

log "starting ${qemu[0]} (${LAB_CPUS} vCPU, ${LAB_MEM} MiB, disk $(basename "$LAB_DISK"))"
"${qemu[@]}" &
qemu_pid=$!
# docker stop is a power cut, not a shutdown: lab.sh down asks the guest to
# shut down first and only then stops the container.
trap 'kill -TERM "$qemu_pid" 2>/dev/null || true' TERM INT
set +e
wait "$qemu_pid"
status=$?
# A trapped signal returns from the first wait early; wait for QEMU itself.
while kill -0 "$qemu_pid" 2>/dev/null; do wait "$qemu_pid"; status=$?; done
log "QEMU exited with status $status"
exit "$status"
