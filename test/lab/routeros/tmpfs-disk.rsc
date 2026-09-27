# tmpfs-disk: a 64 MiB tmpfs disk, which --ephemeral installs into and CHR does not have
#
# `mikroscope install --ephemeral` puts the container's image and root on a
# tmpfs disk, and CHR lists no disk at all (`/disk/print` is empty on the
# lab's clean snapshot). This is the disk doctor's fix line adds, with the
# size it suggests.
#
# Apply with `make lab-profile PROFILE=tmpfs-disk`. Idempotent: a disk in
# slot tmpfs that exists already is kept. What this profile adds carries the
# comment "lab: tmpfs-disk".
:if ([:len [/disk/find slot="tmpfs"]] = 0) do={ /disk/add type=tmpfs tmpfs-max-size=64M slot=tmpfs comment="lab: tmpfs-disk" }
