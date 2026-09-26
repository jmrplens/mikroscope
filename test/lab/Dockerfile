# The virtual RouterOS lab's runner: QEMU and the few tools the lab drives it
# with, so the lab itself needs no more than Docker and flock on the host. One
# image serves both architectures: qemu-system-x86 runs CHR x86_64 under KVM,
# qemu-system-arm (plus the edk2 firmware from qemu-efi-aarch64) runs CHR
# arm64, which boots only through UEFI. bsdtar (libarchive-tools) reads the
# installer's kernel out of RouterOS x86's ISO for the opt-in ISO lab, and nft
# (nftables) closes the lab's namespace to the host's network. See
# test/lab/README.md.
#
# The base is a dated tag, as the compose files pin theirs: trixie-slim was
# trixie-20260918-slim when the lab's timings were measured.
FROM debian:trixie-20260918-slim

RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      qemu-system-x86 qemu-system-arm qemu-efi-aarch64 qemu-utils \
      iproute2 nftables socat openssh-client sshpass curl ca-certificates unzip \
      libarchive-tools \
 && rm -rf /var/lib/apt/lists/*

COPY vm/ /lab/vm/
ENTRYPOINT ["/lab/vm/entrypoint.sh"]
