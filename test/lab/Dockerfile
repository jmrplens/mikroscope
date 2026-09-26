# The virtual RouterOS lab's runner: QEMU and the few tools the lab drives it
# with, so the host needs Docker and nothing else. One image serves both
# architectures: qemu-system-x86 runs CHR x86_64 under KVM, qemu-system-arm
# (plus the AAVMF firmware from qemu-efi-aarch64) runs CHR arm64, which boots
# only through UEFI. See test/lab/README.md.
FROM debian:trixie-slim

RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      qemu-system-x86 qemu-system-arm qemu-efi-aarch64 qemu-utils \
      iproute2 socat openssh-client sshpass curl ca-certificates unzip \
 && rm -rf /var/lib/apt/lists/*

COPY vm/ /lab/vm/
ENTRYPOINT ["/lab/vm/entrypoint.sh"]
