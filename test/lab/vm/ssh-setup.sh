#!/bin/bash
# Installs the lab's key and ssh_config as root's own, with the modes ssh
# insists on: the files are bind-mounted from the host, where their owner is
# whoever ran lab.sh, and ssh refuses a config file root does not own.
set -euo pipefail
[ -f /lab/ssh/id_ed25519 ] || exit 0
install -d -m 700 /root/.ssh
install -m 600 /lab/ssh/id_ed25519 /root/.ssh/id_ed25519
install -m 600 /lab/vm/ssh_config /root/.ssh/config
