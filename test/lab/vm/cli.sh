#!/bin/bash
# What `lab.sh cli` runs: a throwaway container in the lab's network namespace,
# with the mikroscope binary under test mounted in, root's ssh set up for the
# `lab` alias, and nothing from the host's environment but what lab.sh passes.
set -euo pipefail
/lab/vm/ssh-setup.sh
exec "$@"
