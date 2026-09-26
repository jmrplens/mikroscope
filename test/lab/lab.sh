#!/usr/bin/env bash
# lab.sh — the virtual RouterOS lab: MikroTik's Cloud Hosted Router under QEMU
# in a Docker container, provisioned once into a clean snapshot (container
# package installed, device-mode container=yes confirmed) and reset to it in
# seconds. Every RouterOS test mikroscope runs goes here, never to a real
# router. The README next to this file says what it is, what it needs, what
# each step took and where it stops being a router.
#
#   lab.sh up | down | reset | status | provision | fetch | image
#   lab.sh residue              what an install could have left on the router
#   lab.sh export [terse]       the router's /export without its # lines, to stdout
#   lab.sh ssh [command]        the router's console, or one command
#   lab.sh cli <verb> [flags]   the mikroscope CLI, from the lab's LAN side
#   lab.sh put <file> [name]    upload a file to the router
#   lab.sh import <file.rsc>    upload a RouterOS script, /import it, delete it
#   lab.sh profile [name ...]   import routeros/<name>.rsc; no name lists them
#   lab.sh lock <command ...>   run a command while holding this lab's lock
#   lab.sh console              the serial console (Ctrl-] to leave)
#   lab.sh power-cycle          pull the power and put it back
#
# Settings, from the environment (the Makefile passes them through):
#   LAB_ARCH   x86_64 (default; KVM) or arm64 (UEFI, TCG on an x86 host)
#   LAB_ROS    RouterOS version, default 7.24.4
#   LAB_KIND   chr (default), or iso: RouterOS x86 installed from MikroTik's
#              installation ISO, x86_64 only, opt-in and never in CI
#   LAB_KVM    auto (default), require (fail without /dev/kvm) or off (TCG)
#   LAB_STATE_DIR   where .cache/ and .env live; default this directory. Point
#              it at another checkout's test/lab to drive the lab it runs.
#   LAB_LOCK_WAIT   seconds to wait while another process drives the lab;
#              unset waits as long as it takes, 0 fails at once
#   LAB_MEM, LAB_CPUS, LAB_DISK_SIZE   the VM: 1024 MiB, 2 vCPU, 1G disk
#   LAB_CPU    arm64's emulated CPU model, default cortex-a72 (the RB5009's)
#   LAB_AGENT_ROUTES, LAB_AGENT_TARGET   see below; 172.30.0.0/16, 172.30.10.2:9123
#   MIKROSCOPE_BIN   the CLI `lab.sh cli` runs: default this checkout's
#              bin/mikroscope (make build), else the one on PATH
#   LAB_CLI_TOKEN   `lab` hands `lab.sh cli` the lab's agent token as
#              MIKROSCOPE_TOKEN, for --expose without --token on a command line
set -euo pipefail

LAB_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
REPO=$(cd "$LAB_DIR/../.." && pwd)
LAB_ARCH=${LAB_ARCH:-x86_64}
LAB_ROS=${LAB_ROS:-7.24.4}
LAB_KIND=${LAB_KIND:-chr}
LAB_KVM=${LAB_KVM:-auto}
LAB_MEM=${LAB_MEM:-1024}
LAB_CPUS=${LAB_CPUS:-2}
LAB_DISK_SIZE=${LAB_DISK_SIZE:-1G}
LAB_IMAGE=${LAB_IMAGE:-mikroscope-lab:local}
LAB_DL=${LAB_DL:-https://download.mikrotik.com/routeros}
# What the lab's namespace routes to the router, and what the host's agent
# port forwards to. Both take effect when the container is created (up after
# down, or reset). A --subnet outside the routes would leave the namespace by
# its default route, towards the host's own gateway: widen the routes first.
LAB_AGENT_ROUTES=${LAB_AGENT_ROUTES:-172.30.0.0/16}
LAB_AGENT_TARGET=${LAB_AGENT_TARGET:-172.30.10.2:9123}

T0=$(date +%s)
say() { printf '[lab %s +%4ss] %s\n' "${ID:-$LAB_ARCH}" "$(($(date +%s) - T0))" "$*" >&2; }
die() {
	say "error: $*"
	exit 1
}

# ID names one lab: its lock, its disks and, through `short`, its container.
case "$LAB_KIND:$LAB_ARCH" in
chr:x86_64)
	ID=x86_64 short=x86 port_suffix=1
	downloads=("chr-$LAB_ROS.img.zip" "all_packages-x86-$LAB_ROS.zip")
	;;
chr:arm64)
	ID=arm64 short=arm64 port_suffix=2
	downloads=("chr-$LAB_ROS-arm64.img.zip" "all_packages-arm64-$LAB_ROS.zip")
	;;
iso:x86_64)
	ID=x86_64-iso short=x86-iso port_suffix=3
	downloads=("mikrotik-$LAB_ROS.iso")
	;;
iso:arm64)
	die "LAB_KIND=iso is wired for x86_64 only (MikroTik also publishes mikrotik-<v>-arm64.iso; nothing here installs it)"
	;;
*)
	echo "lab: LAB_KIND must be chr or iso and LAB_ARCH x86_64 or arm64, got $LAB_KIND and $LAB_ARCH" >&2
	exit 2
	;;
esac

# Where the lab keeps its state: downloads, disks, the ssh key and .env. Any
# checkout of the repository drives the lab another checkout runs by pointing
# LAB_STATE_DIR at that checkout's test/lab; unset, it is this directory.
if [ -n "${LAB_STATE_DIR:-}" ]; then
	[ -d "$LAB_STATE_DIR" ] || die "LAB_STATE_DIR=$LAB_STATE_DIR is not a directory"
	LAB_STATE_DIR=$(cd "$LAB_STATE_DIR" && pwd)
else
	LAB_STATE_DIR=$LAB_DIR
fi
NAME=${LAB_NAME:-mikroscope-lab-$short}
CACHE=$LAB_STATE_DIR/.cache
DL=$CACHE/downloads/$LAB_ROS
VMREL=vm/$ID-$LAB_ROS
VM=$CACHE/$VMREL
SSHD=$CACHE/ssh
ENV_FILE=$LAB_STATE_DIR/.env
LOCK=$CACHE/$ID.lock

# Host ports, on 127.0.0.1 only: 220N ssh, 800N WebFig, 870N API, 910N agent,
# with N = 1 for x86_64, 2 for arm64 and 3 for the ISO lab, so they can all
# run side by side.
P_SSH=220$port_suffix
P_HTTP=800$port_suffix
P_API=870$port_suffix
P_AGENT=910$port_suffix

# ─── Credentials ─────────────────────────────────────────────────────────────

# .env holds the router's admin password (WebFig, the API, a password login)
# and a bearer token for `install --expose` tests. Generated on first use and
# never printed: `lab.sh env` shows where it is, not what is in it.
load_env() {
	if [ ! -f "$ENV_FILE" ]; then
		umask 077
		{
			echo "# The virtual RouterOS lab's credentials, generated by lab.sh on $(date -u +%Y-%m-%d)."
			echo "# Gitignored. The lab router is reachable only from this machine's loopback."
			echo "LAB_ADMIN_USER=admin"
			echo "LAB_ADMIN_PASSWORD=$(rand 24)"
			echo "LAB_AGENT_TOKEN=$(rand 32)"
		} >"$ENV_FILE"
		say "wrote $ENV_FILE (admin password and agent token; mode 0600)"
	fi
	# shellcheck disable=SC1090
	. "$ENV_FILE"
}

rand() { LC_ALL=C tr -dc 'A-Za-z0-9' </dev/urandom | head -c "$1"; }

ensure_key() {
	if [ ! -f "$SSHD/id_ed25519" ]; then
		mkdir -p "$SSHD"
		chmod 700 "$SSHD"
		# ssh-keygen wants a passwd entry for its uid, so it runs as root and
		# hands the key to whoever runs lab.sh.
		docker run --rm --entrypoint sh -v "$SSHD:/out" "$LAB_IMAGE" -c \
			"ssh-keygen -q -t ed25519 -N '' -C mikroscope-lab -f /out/id_ed25519 && chown $(id -u):$(id -g) /out/id_ed25519 /out/id_ed25519.pub"
		say "generated the lab's ssh key in $SSHD"
	fi
}

# ─── One driver at a time ────────────────────────────────────────────────────

# take_lock keeps two drivers of one lab apart, across processes and
# checkouts: an flock on $CACHE/<id>.lock, held until lab.sh exits. A process
# that drives the lab for a whole session (`lab.sh lock go test …`, the WebFig
# captures) exports LAB_LOCK_HELD, and every lab.sh it starts finds its own
# lock there instead of waiting for itself. The file says who holds it: a
# pid, a verb and a directory, never the arguments, which can carry the lab's
# agent token.
take_lock() {
	case ":${LAB_LOCK_HELD:-}:" in *":$LOCK:"*) return 0 ;; esac
	command -v flock >/dev/null 2>&1 || die "flock (util-linux) is not on PATH; the lab needs it to keep two drivers apart"
	mkdir -p "$CACHE"
	# Both checked: this runs inside `guard_state && take_lock && …`, where
	# set -e does not stop anything, and a lock file this user cannot open
	# would otherwise let the verb go on without the lock.
	exec 9>>"$LOCK" || die "cannot open $LOCK, so $NAME cannot be locked: whoever drives the lab must be able to write its lock file"
	if ! flock -n 9; then
		say "$NAME is in use: $(cat "$LOCK" 2>/dev/null || echo 'holder unknown'); waiting${LAB_LOCK_WAIT:+ up to ${LAB_LOCK_WAIT}s}"
		if [ -n "${LAB_LOCK_WAIT:-}" ]; then
			flock -w "$LAB_LOCK_WAIT" 9 || die "$NAME was still in use after ${LAB_LOCK_WAIT}s"
		else
			flock 9 || die "could not lock $LOCK"
		fi
	fi
	printf 'pid %s (%s), lab.sh %s%s, since %s, in %s\n' "$$" "$(id -un)" "$verb" \
		"${1:+ $(basename "$1")}" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$PWD" >"$LOCK"
	export LAB_LOCK_HELD=${LAB_LOCK_HELD:+$LAB_LOCK_HELD:}$LOCK
}

lock_state() {
	if [ ! -e "$LOCK" ] || ! command -v flock >/dev/null 2>&1; then
		echo free
	elif flock -n "$LOCK" true 2>/dev/null; then
		echo free
	else
		echo "held by $(cat "$LOCK")"
	fi
}

# state_elsewhere prints the LAB_STATE_DIR of an existing container when it
# is not this one's.
state_elsewhere() {
	[ "$(state)" != absent ] || return 0
	local src
	src=$(docker inspect -f '{{range .Mounts}}{{if eq .Destination "/cache"}}{{.Source}}{{end}}{{end}}' "$NAME")
	src=${src%/.cache}
	if [ -n "$src" ] && [ -d "$src" ] && [ "$(cd "$src" && pwd)" != "$LAB_STATE_DIR" ]; then
		echo "$src"
	fi
}

# guard_state refuses to drive a container whose state lives in another
# LAB_STATE_DIR: its key, password and disks are there, not here.
guard_state() {
	local src
	src=$(state_elsewhere)
	[ -z "$src" ] || die "$NAME keeps its state in $src, not in $LAB_STATE_DIR: export LAB_STATE_DIR=$src"
}

# guard_ros refuses to drive a container that runs another LAB_ROS: there is
# one lab per arch at a time, and a test must not run against a version it
# did not ask for.
guard_ros() {
	[ "$(state)" != absent ] || return 0
	local ros
	ros=$(docker inspect -f '{{index .Config.Labels "mikroscope.lab.ros"}}' "$NAME")
	[ "$ros" = "$LAB_ROS" ] || die "$NAME runs RouterOS $ros, not LAB_ROS=$LAB_ROS: lab.sh down first; there is one lab per arch at a time"
}

# ─── Image, downloads, disk ──────────────────────────────────────────────────

# The image is rebuilt whenever its Dockerfile or the scripts it carries
# change, so no checkout runs a lab on an image another checkout built from
# older files.
image_hash() { cat "$LAB_DIR/Dockerfile" "$LAB_DIR"/vm/* | sha256sum | cut -c1-16; }

cmd_image() {
	say "building $LAB_IMAGE"
	docker build -q --label "mikroscope.lab.hash=$(image_hash)" -t "$LAB_IMAGE" "$LAB_DIR" >/dev/null
}

ensure_image() {
	local have
	have=$(docker image inspect -f '{{index .Config.Labels "mikroscope.lab.hash"}}' "$LAB_IMAGE" 2>/dev/null || true)
	[ "$have" = "$(image_hash)" ] || cmd_image
}

# tool runs one command in a throwaway container with the cache mounted, as
# the user running lab.sh, so what it writes under .cache is theirs to delete.
tool() {
	docker run --rm --user "$(id -u):$(id -g)" -v "$CACHE:/cache" -w /cache --entrypoint bash "$LAB_IMAGE" -c "$1"
}

# fetch downloads from MikroTik and checks each file against the .sha256 that
# MikroTik publishes beside it on download.mikrotik.com. Both come from the
# same server over HTTPS, so the check catches a damaged or truncated
# download, not a compromised origin; RouterOS itself verifies the signature
# of every .npk it installs. A cut connection is retried and resumes: one
# download of the ISO was reset at 62 of its 71 MB on 2026-09-26.
cmd_fetch() {
	ensure_image
	mkdir -p "$DL"
	local f sums=()
	for f in "${downloads[@]}"; do
		sums+=("'$f.sha256'")
		if [ -f "$DL/$f" ] && [ -f "$DL/$f.sha256" ]; then
			continue
		fi
		say "downloading $LAB_DL/$LAB_ROS/$f"
		tool "set -e; cd downloads/$LAB_ROS
			curl -fsSL --retry 5 --retry-all-errors --retry-delay 2 -C - -o '$f.part' '$LAB_DL/$LAB_ROS/$f'
			curl -fsSL --retry 5 --retry-all-errors --retry-delay 2 -o '$f.sha256' '$LAB_DL/$LAB_ROS/$f.sha256'
			mv '$f.part' '$f'"
	done
	tool "set -e; cd downloads/$LAB_ROS; sha256sum -c ${sums[*]}" >&2 ||
		die "checksum mismatch: delete $DL and fetch again"
}

# disk makes base.qcow2 and the container package. For CHR, base.qcow2 is
# MikroTik's raw image converted and grown to LAB_DISK_SIZE, and
# container-<v>.npk comes out of the extra-packages archive. For the ISO lab
# it is MikroTik's installer run onto an empty disk, with the container
# package picked in its menu.
cmd_disk() {
	cmd_fetch
	mkdir -p "$VM"
	if [ "$LAB_KIND" = iso ]; then
		iso_install
		return 0
	fi
	local img_zip=${downloads[0]} pkg_zip=${downloads[1]}
	if [ ! -f "$VM/base.qcow2" ]; then
		say "converting $img_zip to base.qcow2 ($LAB_DISK_SIZE)"
		tool "set -e; tmp=\$(mktemp -d)
			unzip -q -o 'downloads/$LAB_ROS/$img_zip' -d \$tmp
			qemu-img convert -O qcow2 \$tmp/*.img '$VMREL/base.qcow2'
			qemu-img resize -q '$VMREL/base.qcow2' '$LAB_DISK_SIZE'
			chmod a-w '$VMREL/base.qcow2'
			rm -rf \$tmp"
	fi
	if [ ! -f "$VM/container-$LAB_ROS.npk" ]; then
		tool "set -e; unzip -q -o -j 'downloads/$LAB_ROS/$pkg_zip' 'container-$LAB_ROS*.npk' -d '$VMREL'
			cd '$VMREL'; for f in container-*.npk; do [ \"\$f\" = container-$LAB_ROS.npk ] || mv \"\$f\" container-$LAB_ROS.npk; done"
		[ -f "$VM/container-$LAB_ROS.npk" ] || die "no container package in $pkg_zip"
	fi
}

# overlay makes $2 a copy-on-write layer over $1, by a relative backing path so
# the chain reads the same inside the container and out.
overlay() {
	tool "set -e; cd '$VMREL'; rm -f '$2'; qemu-img create -q -f qcow2 -b '$1' -F qcow2 '$2'"
}

# ─── The machine ─────────────────────────────────────────────────────────────

# docker inspect prints an empty line for a container that does not exist.
state() { docker inspect -f '{{.State.Status}}' "$NAME" 2>/dev/null | grep . || echo absent; }

# start runs the container on the given disk, or powers an existing one back
# on. The scripts the container runs are copied into the state directory
# first, so the lab keeps powering on after the checkout that created it is
# gone.
start() {
	local disk=$1
	case "$(state)" in
	running) return 0 ;;
	exited | created)
		docker start "$NAME" >/dev/null
		return 0
		;;
	esac
	# KVM runs a guest of the host's own architecture only: x86_64 on an x86
	# host, arm64 on an arm64 one. A lab that is emulated gets no /dev/kvm it
	# could not use.
	local kvm=() native=0
	case "$LAB_ARCH:$(uname -m)" in x86_64:x86_64 | arm64:aarch64) native=1 ;; esac
	case "$LAB_KVM" in
	auto) [ "$native" = 1 ] && [ -e /dev/kvm ] && kvm=(--device /dev/kvm) ;;
	require)
		[ "$native" = 1 ] || die "LAB_KVM=require: the $LAB_ARCH lab gets KVM only on a $LAB_ARCH host, and this one is $(uname -m)"
		[ -e /dev/kvm ] || die "LAB_KVM=require and this host has no /dev/kvm"
		kvm=(--device /dev/kvm)
		;;
	off) ;;
	*) die "LAB_KVM must be auto, require or off, got $LAB_KVM" ;;
	esac
	local stage=$CACHE/run/$NAME
	rm -rf "$stage"
	mkdir -p "$stage"
	cp -R "$LAB_DIR/vm" "$stage/vm"
	docker run -d --name "$NAME" --hostname "$NAME" \
		--label org.opencontainers.image.source=https://github.com/jmrplens/mikroscope \
		--label mikroscope.lab.arch="$LAB_ARCH" --label mikroscope.lab.ros="$LAB_ROS" \
		--label mikroscope.lab.kind="$LAB_KIND" \
		--cap-add NET_ADMIN --device /dev/net/tun "${kvm[@]}" \
		-e LAB_ARCH="$LAB_ARCH" -e LAB_KIND="$LAB_KIND" -e LAB_KVM="$LAB_KVM" \
		-e LAB_MEM="$LAB_MEM" -e LAB_CPUS="$LAB_CPUS" -e LAB_CPU="${LAB_CPU:-}" \
		-e LAB_AGENT_ROUTES="$LAB_AGENT_ROUTES" -e LAB_AGENT_TARGET="$LAB_AGENT_TARGET" \
		-e LAB_DISK="/cache/$VMREL/$disk" -e LAB_INSTALLER="${LAB_INSTALLER:-}" \
		-e LAB_CONSOLE_LOG="/cache/$VMREL/console.log" \
		-v "$CACHE:/cache" -v "$stage/vm:/lab/vm:ro" -v "$SSHD:/lab/ssh:ro" \
		-p "127.0.0.1:$P_SSH:22" -p "127.0.0.1:$P_HTTP:80" \
		-p "127.0.0.1:$P_API:8728" -p "127.0.0.1:$P_AGENT:9123" \
		"$LAB_IMAGE" >/dev/null
}

# inlab runs a command inside the lab container, in its network namespace.
inlab() { docker exec -i "$NAME" "$@"; }

# ros runs one RouterOS command over ssh, from the LAN side.
ros() { inlab ssh lab "$1"; }

# wait_ssh waits until the router answers on ssh by the given alias.
wait_ssh() {
	local alias=${1:-lab} limit=${2:-300} i=0
	while ! inlab ssh -o ConnectTimeout=2 "$alias" ':put ok' >/dev/null 2>&1; do
		[ "$(state)" = running ] || die "the lab container stopped: docker logs $NAME; tail $VM/console.log"
		i=$((i + 3))
		[ $i -lt "$limit" ] || die "no ssh from the router after ${limit}s"
		sleep 1
	done
}

# wait_down waits for the container to stop, which is the guest powering off.
wait_down() {
	local limit=${1:-120} i=0
	while [ "$(state)" = running ]; do
		i=$((i + 1))
		[ $i -lt "$limit" ] || return 1
		sleep 1
	done
}

monitor() { inlab bash -c "printf '%s\n' '$1' | socat - UNIX-CONNECT:/run/lab/monitor.sock" >/dev/null 2>&1 || true; }

# ─── The serial console, driven ──────────────────────────────────────────────

# The ISO lab has no ssh until a console session gives ether1 an address, and
# MikroTik's installer is a menu on the same console. These read the log QEMU
# writes of the console and type into its socket. The mark is a byte offset
# in the log: con_text prints what came after it, without the terminal's
# escape sequences.
CON_MARK=0
con_log() { echo "$VM/console.log"; }
con_size() { stat -c %s "$(con_log)" 2>/dev/null || echo 0; }
con_mark() { CON_MARK=$(con_size); }
con_text() {
	local end=${1:-$(con_size)}
	tail -c +"$((CON_MARK + 1))" "$(con_log)" 2>/dev/null | head -c "$((end - CON_MARK))" |
		sed 's/\x1b\[[0-9;?]*[A-Za-z]//g; s/\x1b[78cZ]//g' | tr -d '\r'
}
# con_wait waits for an extended regular expression after the mark.
con_wait() {
	local re=$1 limit=${2:-60} i=0
	until con_text | grep -qE -- "$re"; do
		[ "$(state)" = running ] || die "the lab container stopped while waiting for '$re' on the console: $(con_log)"
		i=$((i + 1))
		[ $i -lt $((limit * 2)) ] || die "no '$re' on the console after ${limit}s: $(con_log)"
		sleep 0.5
	done
}
# con_type types slowly: RouterOS's serial console drops characters that
# arrive back to back. Typed at once, `admin+cte` arrived as `admin+ct` and
# commands lost characters mid-word; 30 ms apart, nothing was lost.
con_type() {
	docker exec -i -e S="$1" "$NAME" bash -c \
		'for ((i = 0; i < ${#S}; i++)); do printf "%s" "${S:i:1}"; sleep 0.03; done | socat -u - UNIX-CONNECT:/run/lab/console.sock'
}

# ─── RouterOS x86 from the installation ISO (LAB_KIND=iso) ───────────────────

# iso_install runs MikroTik's installer onto an empty disk, which becomes
# base.qcow2. The entrypoint boots the ISO's own kernel with the ISO's own
# command line plus console=ttyS0, which puts the installer's menu on the
# serial console instead of the VGA screen, and makes the disk SATA: on
# virtio-blk the installer answered "getHardwareID: could not get disk
# /dev/vda info" and "no valid harddrives found", with and without a serial
# number on the disk (7.24.4, 2026-09-26). The menu starts on `system`, which
# is always installed; `n` walks down until the description line names
# `container`, space selects it (without redrawing anything), `i` installs
# and `y` agrees that the disk is erased.
iso_install() {
	[ ! -f "$VM/base.qcow2" ] || return 0
	say "installing RouterOS $LAB_ROS from ${downloads[0]} onto an empty $LAB_DISK_SIZE disk"
	tool "set -e; rm -f '$VMREL/install.qcow2'; qemu-img create -q -f qcow2 '$VMREL/install.qcow2' '$LAB_DISK_SIZE'"
	[ "$(state)" = absent ] || docker rm -f "$NAME" >/dev/null
	: >"$(con_log)"
	CON_MARK=0
	LAB_INSTALLER=/cache/downloads/$LAB_ROS/${downloads[0]} start install.qcow2
	con_wait 'to install locally' 120
	local i found=0
	for i in $(seq 1 30); do
		con_mark
		con_type n
		con_wait '\(depends on' 10
		if con_text | grep -qE '(^|[^a-z-])container \(depends on'; then
			found=1
			break
		fi
	done
	[ "$found" = 1 ] || die "no container package in the installer's menu after $i steps: $(con_log)"
	# Space toggles the package without redrawing the menu; `i` redraws it
	# with the selection before it asks to go on.
	con_mark
	con_type ' '
	sleep 0.5
	con_type i
	con_wait 'Continue\? \[y/n\]|keep old configuration' 30
	con_text | grep -q '\[X\] container' || die "the container package is not selected in the installer's menu: $(con_log)"
	if con_text | grep -q 'keep old configuration'; then
		con_mark
		con_type n
		con_wait 'Continue\? \[y/n\]' 30
	fi
	con_mark
	con_type y
	con_wait 'Software installed' 300
	local pkgs
	pkgs=$(con_text | grep -o 'installing [a-z0-9.-]*' | sort -u | sed 's/^installing //' | tr '\n' ' ')
	say "installed: $pkgs"
	case "$pkgs" in *container-*) ;; *) die "the installer did not install the container package: $(con_log)" ;; esac
	# The installer asks for Enter to reboot, and the reboot would start its
	# kernel again: the power is pulled instead.
	monitor quit
	wait_down 30 || die "QEMU did not quit after the install"
	docker rm "$NAME" >/dev/null
	tool "set -e; cd '$VMREL'; mv install.qcow2 base.qcow2; chmod a-w base.qcow2"
}

# iso_first_login gives a freshly installed RouterOS x86 what CHR ships with,
# a DHCP client on ether1, so the lab's first-boot path (ssh as admin with the
# empty password through QEMU's forward) works from there on as it does for
# CHR. After the install no interface has an address and the console is the
# only way in. The login name `admin+ct` turns off colours and the terminal
# probe; then, on 7.24.4, the licence question, the no-key notice ("You have
# 23h49m to configure the router to be remotely accessible") and the password
# change come in turn. Ctrl-C skips the password change: the password is set
# over ssh, as for CHR, so it never crosses the console log.
iso_first_login() {
	con_wait 'Login: *$' 300
	con_mark
	con_type 'admin+ct'$'\r'
	con_wait 'Password: *$' 30
	con_mark
	con_type $'\r'
	local out end n=0 notice=""
	while :; do
		sleep 1
		end=$(con_size)
		out=$(con_text "$end")
		case "$out" in
		*'software license? [Y/n]'*) CON_MARK=$end && con_type n ;;
		*'Please press "Enter" to continue'*)
			notice=$(printf '%s\n' "$out" | grep -m1 -o 'You have [0-9hm]* to configure the router' || true)
			CON_MARK=$end && con_type $'\r'
			;;
		*'new password>'*) CON_MARK=$end && con_type $'\003' ;;
		*'] >'*) break ;;
		esac
		n=$((n + 1))
		[ $n -lt 90 ] || die "no RouterOS prompt on the console after the login: $(con_log)"
	done
	say "RouterOS x86 with no key: ${notice:-no notice seen}"
	con_mark
	con_type '/ip/dhcp-client/add interface=ether1 comment="lab: what CHR ships"'$'\r'
	con_wait '\] >' 30
	con_type '/quit'$'\r'
}

# ─── Provisioning ────────────────────────────────────────────────────────────

# provision builds clean.qcow2: a fresh router with the lab's access
# (password, key, LAN address on ether2), the container package installed and
# device-mode container=yes confirmed by a cold reboot. Nothing else is
# configured: interface lists, address lists and firewall stay as RouterOS
# ships them, so what a test needs beyond that it sets up itself — or finds
# out, as a user would, from `mikroscope doctor`.
cmd_provision() {
	if [ -f "$VM/clean.qcow2" ] && [ "${FORCE:-0}" != 1 ]; then
		say "clean snapshot exists ($VM/clean.qcow2); FORCE=1 to rebuild it"
		return 0
	fi
	ensure_image
	load_env
	ensure_key
	cmd_disk
	[ "$(state)" = absent ] || docker rm -f "$NAME" >/dev/null
	overlay base.qcow2 provision.qcow2
	: >"$(con_log)"
	CON_MARK=0

	say "first boot of RouterOS $LAB_ROS ($LAB_KIND $LAB_ARCH)"
	start provision.qcow2
	[ "$LAB_KIND" != iso ] || iso_first_login
	local i=0
	while ! inlab sshpass -p '' ssh -o ConnectTimeout=3 -o PubkeyAuthentication=no lab-wan ':put ok' >/dev/null 2>&1; do
		[ "$(state)" = running ] || die "the lab container stopped during first boot: docker logs $NAME"
		i=$((i + 3))
		[ $i -lt 600 ] || die "no ssh from the router on ether1 after 600s"
		sleep 3
	done
	say "router up; ssh as admin with the empty password over ether1"

	local pub
	pub=$(cat "$SSHD/id_ed25519.pub")
	# One connect: the LAN address, the identity, the key, then the password,
	# which ends the empty-password login this session is using.
	inlab sshpass -p '' ssh -o PubkeyAuthentication=no lab-wan \
		"/system/identity/set name=mikroscope-lab-$short; /ip/address/add address=192.168.88.1/24 interface=ether2 comment=\"lab LAN\"; /user/ssh-keys/add user=admin key=\"$pub\"; /user/set [find name=admin] password=\"$LAB_ADMIN_PASSWORD\"" >/dev/null
	wait_ssh lab 60
	say "ssh by key over ether2 (LAN) works; password set"

	local pkg
	pkg=$(ros ':put [:len [/system/package/find name="container" disabled=no]]' | tr -d '\r')
	if [ "$pkg" = 1 ]; then
		say "container package already installed"
	else
		say "uploading container-$LAB_ROS.npk"
		inlab scp -q "/cache/$VMREL/container-$LAB_ROS.npk" "lab:container-$LAB_ROS.npk"
		ros '/system/reboot' >/dev/null 2>&1 || true
		sleep 5
		wait_ssh lab 300
		pkg=$(ros ':put [:len [/system/package/find name="container" disabled=no]]' | tr -d '\r')
		[ "$pkg" = 1 ] || die "the container package is not installed after the reboot (found=$pkg)"
		say "container package installed"
	fi

	cmd_device_mode
	say "device-mode container=yes confirmed"

	say "shutting the router down for the snapshot"
	ros '/system/shutdown' >/dev/null 2>&1 || true
	wait_down 120 || die "the router did not power off within 120s"
	docker rm "$NAME" >/dev/null
	mv -f "$VM/provision.qcow2" "$VM/clean.qcow2"
	chmod a-w "$VM/clean.qcow2"
	overlay clean.qcow2 run.qcow2
	say "clean snapshot: $VM/clean.qcow2 (over base.qcow2); run.qcow2 is the live layer"
}

# device_mode asks for container=yes and confirms it the way MikroTik
# documents for a device with no button to press: a cold reboot inside the
# activation window (activation-timeout, 5 min by default; MikroTik's
# Device-mode page: "perform a 'cold reboot' - that is, unplug the power"). The
# update command holds its console while it waits — on CHR 7.24.4 it prints
# `update: turn off power in 5m to activate changes`, not the documented
# `please activate by turning power off or pressing reset or mode button` —
# and the power is pulled under it (QEMU quits) and put back (docker start).
# MikroTik counts update attempts and allows three before a power cycle resets
# the counter; each provision is a fresh disk, so it starts at zero.
cmd_device_mode() {
	local now
	now=$(ros ':put [/system/device-mode/get container]' | tr -d '\r')
	if [ "$now" = yes ] || [ "$now" = true ]; then
		say "device-mode container is already yes"
		return 0
	fi
	inlab bash -c 'ssh lab "/system/device-mode/update container=yes" >/run/lab/device-mode.out 2>&1 &'
	local i=0
	until inlab grep -qi 'activate\|power' /run/lab/device-mode.out 2>/dev/null; do
		i=$((i + 1))
		[ $i -lt 30 ] || {
			inlab cat /run/lab/device-mode.out >&2 || true
			die "device-mode update never asked for its confirmation"
		}
		sleep 1
	done
	say "RouterOS: $(inlab cat /run/lab/device-mode.out | tr -d '\r' | grep -i -m1 'activate\|power' | sed 's/ *--.*//')"
	say "pulling the power (QEMU quit) and putting it back"
	monitor quit
	wait_down 30 || die "QEMU did not quit"
	docker start "$NAME" >/dev/null
	sleep 5
	wait_ssh lab 300
	# MikroTik writes that a confirmed change reboots the device by itself. On
	# CHR 7.24.4 (2026-09-26) the boot after the power cut was the only one:
	# the console showed no further boot, and the first ssh after it already
	# read container=true.
	now=$(ros ':put [/system/device-mode/get container]' | tr -d '\r')
	[ "$now" = yes ] || [ "$now" = true ] || die "device-mode container=$now after the power cycle"
}

# ─── Verbs ───────────────────────────────────────────────────────────────────

cmd_up() {
	ensure_image
	load_env
	ensure_key
	[ -f "$VM/clean.qcow2" ] || cmd_provision
	[ -f "$VM/run.qcow2" ] || overlay clean.qcow2 run.qcow2
	if [ "$(state)" != running ]; then
		say "starting $NAME"
		start run.qcow2
	fi
	wait_ssh lab 300
	agent_routes_end_here
	say "up: ssh 127.0.0.1:$P_SSH, WebFig http://127.0.0.1:$P_HTTP, API 127.0.0.1:$P_API, agent 127.0.0.1:$P_AGENT (-> $LAB_AGENT_TARGET)"
}

# agent_routes_end_here gives the router a blackhole route for each of
# LAB_AGENT_ROUTES, so a packet for an agent address the router has no veth
# for is dropped there instead of leaving by the default route on ether1.
#
# On a real router that packet goes to the ISP and dies. In the lab, ether1 is
# QEMU's user networking, which opens a socket for every connection the guest
# makes, in the lab container's namespace — where LAB_AGENT_ROUTES lead back to
# the router over lan0. The SYN comes back in on ether2, goes out ether1
# again, and every lap is one more socket. One doctor run against a lab with
# no agent installed (its probe of 172.30.10.2:9123) had QEMU's main thread at
# 100 % of a host core and 6,100 sockets in SYN_SENT within two minutes, 7,100
# within four, measured on the arm64 lab on 2026-09-26; the route below
# drained them in 80 s. While it lasted, the emulated router crawled. None of
# those sockets left the namespace: its route for LAB_AGENT_ROUTES points at
# lan0.
#
# An installed agent's /30 is a connected route and more specific, so it
# still wins. The routes are set at every boot rather than baked into the
# snapshot, so they follow LAB_AGENT_ROUTES; they show in /export, commented.
agent_routes_end_here() {
	local cmd='/ip/route/remove [find comment="lab: LAB_AGENT_ROUTES end here"]; ' net
	for net in $LAB_AGENT_ROUTES; do
		cmd+="/ip/route/add dst-address=$net blackhole comment=\"lab: LAB_AGENT_ROUTES end here\"; "
	done
	ros "$cmd" >/dev/null
}

cmd_down() {
	if [ "$(state)" = running ]; then
		say "shutting the router down"
		ros '/system/shutdown' >/dev/null 2>&1 || monitor system_powerdown
		wait_down 90 || {
			say "no power-off after 90s: pulling the power"
			docker stop -t 5 "$NAME" >/dev/null
		}
	fi
	[ "$(state)" = absent ] || docker rm "$NAME" >/dev/null
	say "down (the disk keeps its state; reset discards it)"
}

cmd_reset() {
	[ -f "$VM/clean.qcow2" ] || die "no clean snapshot yet: run lab.sh up (or provision) first"
	[ "$(state)" = absent ] || docker rm -f "$NAME" >/dev/null
	overlay clean.qcow2 run.qcow2
	say "run.qcow2 reset to the clean snapshot"
	cmd_up
}

cmd_power_cycle() {
	[ "$(state)" = running ] || die "$NAME is not running"
	monitor quit
	wait_down 30 || die "QEMU did not quit"
	docker start "$NAME" >/dev/null
	wait_ssh lab 300
	say "power-cycled"
}

cmd_status() {
	local st accel
	st=$(state)
	echo "container: $NAME ($st), image $LAB_IMAGE, RouterOS $LAB_ROS $LAB_KIND $LAB_ARCH"
	local elsewhere
	elsewhere=$(state_elsewhere)
	echo "state:     $LAB_STATE_DIR${elsewhere:+ (the running lab keeps its state in $elsewhere: export LAB_STATE_DIR=$elsewhere)}"
	echo "lock:      $(lock_state)"
	local disks="" d
	for d in "$VM"/*.qcow2; do [ -f "$d" ] && disks+="$(basename "$d") "; done
	echo "disks:     ${disks:-none}"
	[ "$st" = running ] || return 0
	accel=$(docker logs "$NAME" 2>&1 | grep -o 'accel=[a-z]*' | tail -1 || true)
	echo "host:      ssh 127.0.0.1:$P_SSH  WebFig http://127.0.0.1:$P_HTTP  API 127.0.0.1:$P_API  agent 127.0.0.1:$P_AGENT${accel:+  $accel}"
	# One connect for every reading, as on a real router. CHR reports a
	# licence level; RouterOS x86 with no key reports the time it has left.
	# shellcheck disable=SC2016 # $l is RouterOS's variable, not the shell's
	ros ':put ("router:    " . [/system/identity/get name] . ", " . [/system/resource/get board-name] . ", RouterOS " . [/system/resource/get version] . ", " . [/system/resource/get architecture-name] . ", up " . [/system/resource/get uptime]); :put ("memory:    free " . ([/system/resource/get free-memory] / 1048576) . " MiB of " . ([/system/resource/get total-memory] / 1048576) . "; disk free " . ([/system/resource/get free-hdd-space] / 1048576) . " MiB of " . ([/system/resource/get total-hdd-space] / 1048576)); :put ("container: package " . [:len [/system/package/find name="container" disabled=no]] . ", device-mode container=" . [/system/device-mode/get container] . ", containers " . [:len [/container/find]] . ", veths " . [:len [/interface/veth/find]]); :local l [/system/license/get]; :if ([:typeof ($l->"level")] != "nothing") do={ :put ("licence:   " . ($l->"level")) } else={ :put ("licence:   no key, expires in " . ($l->"expires-in")) }' | tr -d '\r'
}

# residue counts, in one connect, everything an install can leave behind:
# what `mikroscope uninstall` verifies and what it does not look at. Compare it
# with a reading taken before the install.
cmd_residue() {
	# shellcheck disable=SC2016 # $f is RouterOS's variable, not the shell's
	ros ':put ("containers " . [:len [/container/find]] . ", envs " . [:len [/container/envs/find]] . ", mounts " . [:len [/container/mounts/find]] . ", veths " . [:len [/interface/veth/find]] . ", ip-addresses " . [:len [/ip/address/find]] . ", list-members " . [:len [/interface/list/member/find]] . ", filter " . [:len [/ip/firewall/filter/find]] . ", nat " . [:len [/ip/firewall/nat/find]] . ", raw " . [:len [/ip/firewall/raw/find]] . ", address-lists " . [:len [/ip/firewall/address-list/find]] . ", disks " . [:len [/disk/find]]); :foreach f in=[/file/find] do={ :put ("file " . [/file/get $f name] . " (" . [/file/get $f type] . ")") }' | tr -d '\r'
}

# export prints the router's configuration without its comment lines, which
# carry the date and the software id, for a test to compare in memory. It
# goes to stdout only; nothing here writes it to a file. RouterOS 7 hides
# sensitive values unless asked, and show-sensitive is refused.
cmd_export() {
	local a
	for a in "$@"; do
		case "$a" in
		terse | verbose | compact) ;;
		*) die "export takes terse, verbose or compact, got $a" ;;
		esac
	done
	ros "/export $*" | tr -d '\r' | grep -v '^#'
}

cmd_ssh() {
	if [ $# -eq 0 ]; then
		docker exec -it "$NAME" ssh lab
	else
		ros "$*"
	fi
}

# cli_bin is the CLI `lab.sh cli` runs: MIKROSCOPE_BIN, else this checkout's
# bin/mikroscope (`make build` builds it with CGO_ENABLED=0, so it runs in
# the lab's Debian image), else the one on PATH. A test run exercises the
# checkout, not whatever release the host has installed.
cli_bin() {
	local bin=${MIKROSCOPE_BIN:-}
	if [ -z "$bin" ]; then
		if [ -x "$REPO/bin/mikroscope" ]; then
			bin=$REPO/bin/mikroscope
		else
			bin=$(command -v mikroscope || true)
		fi
	fi
	[ -n "$bin" ] && [ -x "$bin" ] || die "no mikroscope binary: make build, put one on PATH, or set MIKROSCOPE_BIN"
	echo "$(cd "$(dirname "$bin")" && pwd)/$(basename "$bin")"
}

# cli runs the mikroscope CLI from the lab's LAN side: a throwaway container in
# the lab's network namespace, so `--router lab` is the lab router and the
# agent's veth address is routed to it. Nothing from the host's environment
# reaches it — no MIKROSCOPE_* variable set for a real router can leak in —
# except MIKROSCOPE_ROUTER=lab and, with LAB_CLI_TOKEN=lab, the lab's own agent
# token as MIKROSCOPE_TOKEN. The current directory is mounted read-write at
# its own path and is the working directory, and the repository read-only at
# its own path, so relative paths, and absolute paths inside either, mean in
# the container what they mean on the host (--agent-tar
# build/agent-images/…, --out x.rsc). An absolute path outside both would name
# nothing there, so it is refused.
cmd_cli() {
	local bin ver here=$PWD a v
	bin=$(cli_bin)
	ver=$("$bin" version 2>/dev/null | head -1 || true)
	echo "using $bin: ${ver:-no version line}" >&2
	[ "$(state)" = running ] || die "$NAME is not running: lab.sh up"
	case "$here" in
	/ | /root | /lab | /usr | /etc | /run | /bin | /sbin | /lib | /lib64 | /var | /proc | /sys | /dev | /tmp)
		die "run lab.sh cli from a project directory, not $here: it is mounted over the same path in the CLI's container"
		;;
	esac
	for a in "$@"; do
		v=$a
		case "$a" in --*=*) v=${a#*=} ;; esac
		case "$v" in
		/*)
			case "$v/" in
			"$here"/* | "$REPO"/*) ;;
			*) die "$v is outside $here and $REPO, the two directories lab.sh cli mounts: copy it into one of them" ;;
			esac
			;;
		esac
	done
	local mounts=(-v "$here:$here")
	[ "$here" = "$REPO" ] || mounts+=(-v "$REPO:$REPO:ro")
	local tty=()
	[ -t 0 ] && [ -t 1 ] && tty=(-t)
	# The token travels in the environment: `-e NAME` with no value copies it
	# from this process, so it is on no command line in the host's process
	# table, where `--token <value>` would sit for as long as the CLI runs.
	local envs=(-e MIKROSCOPE_ROUTER=lab)
	if [ "${LAB_CLI_TOKEN:-}" = lab ]; then
		load_env
		export MIKROSCOPE_TOKEN=$LAB_AGENT_TOKEN
		envs+=(-e MIKROSCOPE_TOKEN)
	fi
	docker run --rm -i "${tty[@]}" --network "container:$NAME" \
		-v "$bin:/usr/local/bin/mikroscope:ro" -v "$LAB_DIR/vm:/lab/vm:ro" -v "$SSHD:/lab/ssh:ro" \
		"${mounts[@]}" -w "$here" "${envs[@]}" \
		--entrypoint /lab/vm/cli.sh "$LAB_IMAGE" mikroscope "$@"
}

# put uploads a local file to the router (default: its own name).
cmd_put() {
	local src=$1 dst=${2:-$(basename "$1")}
	[ -f "$src" ] || die "no such file: $src"
	docker cp "$src" "$NAME:/tmp/lab-upload" >/dev/null
	inlab scp -q /tmp/lab-upload "lab:$dst"
	inlab rm -f /tmp/lab-upload
	say "uploaded $src as $dst"
}

# import uploads a RouterOS script, runs it with /import and removes it, so
# the router keeps what the script did and not the file. RouterOS's ssh exits
# 0 whether the script failed or not, so success is read from its message.
cmd_import() {
	local src=$1 name out
	name=lab-$(basename "$src")
	cmd_put "$src" "$name"
	out=$(ros "/import file-name=$name; /file/remove [find name=\"$name\"]" | tr -d '\r')
	case "$out" in
	*"executed successfully"*) say "imported $(basename "$src")" ;;
	*)
		ros "/file/remove [find name=\"$name\"]" >/dev/null 2>&1 || true
		printf '%s\n' "$out" >&2
		die "/import of $(basename "$src") failed"
		;;
	esac
}

# profile imports lab set-ups by name, routeros/<name>.rsc, in the order
# given; with no name it lists them, with the first line of each.
cmd_profile() {
	local p f
	if [ $# -eq 0 ]; then
		for f in "$LAB_DIR"/routeros/*.rsc; do
			printf '%-24s %s\n' "$(basename "$f" .rsc)" "$(sed -n '1s/^# *[a-z0-9-]*: *//p' "$f")"
		done
		return 0
	fi
	for p in "$@"; do
		[ -f "$LAB_DIR/routeros/$p.rsc" ] || die "no profile $p: lab.sh profile lists them"
	done
	for p in "$@"; do
		cmd_import "$LAB_DIR/routeros/$p.rsc"
	done
}

# lock runs a command while holding this lab's lock: a session of lab.sh
# calls no other driver may interleave with, such as a test suite. The command
# does not inherit the lock's descriptor, so nothing it leaves running keeps
# the lab locked once it returns.
cmd_lock() {
	[ $# -gt 0 ] || die "lock needs a command: lab.sh lock <command> [args]"
	local rc=0
	"$@" 9>&- || rc=$?
	return "$rc"
}

cmd_console() {
	echo "serial console of $NAME; Ctrl-] then Enter... leaves (socat: Ctrl-C)" >&2
	docker exec -it "$NAME" socat -,raw,echo=0,escape=0x1d UNIX-CONNECT:/run/lab/console.sock
}

cmd_env() {
	echo "credentials: $ENV_FILE (LAB_ADMIN_USER, LAB_ADMIN_PASSWORD, LAB_AGENT_TOKEN)"
	echo "ssh key:     $SSHD/id_ed25519"
	echo "from the host: ssh -i $SSHD/id_ed25519 -p $P_SSH -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null admin@127.0.0.1"
}

verb=${1:-status}
[ $# -gt 0 ] && shift
# Every verb that drives the VM takes the lab's lock first, then checks that
# the container it would drive is this lab's. status, env, fetch and image do
# not touch the VM, nor does profile with no name, which lists them; lock
# takes the lock and leaves the rest to its command.
case "$verb" in
status | env | fetch | image | help | -h | --help) ;;
lock) take_lock "${1:-}" ;;
down) guard_state && take_lock ;;
profile) [ $# -eq 0 ] || { guard_state && take_lock && guard_ros; } ;;
*) guard_state && take_lock && guard_ros ;;
esac
case "$verb" in
up) cmd_up ;;
down) cmd_down ;;
reset) cmd_reset ;;
status) cmd_status ;;
residue) cmd_residue ;;
export) cmd_export "$@" ;;
provision) cmd_provision ;;
device-mode) cmd_device_mode ;;
fetch) cmd_fetch ;;
disk) cmd_disk ;;
image) cmd_image ;;
ssh) cmd_ssh "$@" ;;
cli) cmd_cli "$@" ;;
put) cmd_put "$@" ;;
import) cmd_import "$@" ;;
profile) cmd_profile "$@" ;;
lock) cmd_lock "$@" ;;
console) cmd_console ;;
power-cycle) cmd_power_cycle ;;
env) cmd_env ;;
*)
	awk 'NR > 1 && /^#/ { sub(/^# ?/, ""); print; next } NR > 1 { exit }' "${BASH_SOURCE[0]}"
	exit 2
	;;
esac
