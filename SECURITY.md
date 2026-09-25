# Security policy

## Reporting a vulnerability

Do not put the details in a public issue or discussion.

Use GitHub's private vulnerability reporting: the **Security** tab of this
repository, then **Report a vulnerability**. It creates a private advisory that
only the maintainer can see. If that button is not there, start a discussion
in [General](https://github.com/jmrplens/mikroscope/discussions/categories/general)
that asks for a private channel and says nothing else about the problem, and
wait there for a reply that names one. Issues go through forms that ask for
the details, so an issue cannot stay that empty.

Please include the version (`mikroscope version`, and the agent's from the
`agent:` line of `mikroscope status` if it differs), the RouterOS version and
board, what an attacker gains, and the smallest way to reproduce it. If a proof
of concept needs a credential, a router address or an export, do not send it:
describe it.

You can expect an acknowledgement within a week and, once the report is
confirmed, a fix released before the advisory is published. A reporter who
wants credit gets it in the advisory; a reporter who does not is not named.

## Supported versions

The latest release. This is a pair of binaries with no long-term branches, so a
fix ships as a new tag rather than as a patch to an older one.

## What is worth reporting

Two things are worth protecting here: the router the agent runs on, and the
credentials the collector holds on the operator's machine. The design, set out
in [docs/security.md](docs/security.md), is that the agent listens and never
connects out, presents no credential, and runs in a container whose only
network is its own veth; and that every write the CLI makes to a router is
listed first, tagged, and removed by exact tag and identity.

Anything that breaks one of those is a vulnerability. Concretely, and in rough
order of interest:

- A flag value, an environment variable or a response from the router that
  changes which RouterOS command the CLI runs, beyond the value it was bounded
  to, or lets `uninstall` or `upgrade` select an object it did not create.
- A way for something that is not the configured collector to reach the agent's
  endpoints, past the bearer token when one is set, or past the address the
  agent binds.
- The agent opening a connection, writing outside its container, or reading
  something the documentation says it does not.
- The RouterOS API password, a sink token, the agent's token or a Grafana token
  appearing in a log line, an error message, a recording, a sink's output or a
  request to any host other than the one it belongs to.
- A crafted agent response, recording or dashboard input that makes the CLI
  execute, open or write something other than what it was asked to.
- A dependency advisory that this project's use actually reaches.

## What is not a vulnerability

- The prerequisites RouterOS imposes: the `container` package, `device-mode
  container=yes`, and a privileged container for the kernel log, slab and MTD
  counters (`--privileged=false` opts out, and the agent's `/capabilities`
  lists the sources it still has). They are documented, and the choice is the
  operator's.
- The agent's bearer token being readable by any RouterOS user with `read`
  policy, because `--token` writes it into the container envlist. That is how
  RouterOS stores it, and the security page says so.
- The agent's metrics, and the collector's `forward --prom` exposition, serving
  the numbers they exist to serve to whoever can reach them. Bind them to an
  address only your collector or Prometheus reaches, or set a token.
- What `--expose` opens, as documented: a tagged dst-nat rule and a tagged
  forward accept that make the agent reachable on the router's LAN address.
  `install --expose` requires a token, but an `upgrade` of that install without
  `--token` keeps the dst-nat and drops the token. `mikroscope doctor` warns
  about that state (`WARN … token=unset`), and it is a configuration, not a
  vulnerability. A way to reach it without an explicit upgrade is worth
  reporting.
- The cost of observing: the agent uses CPU and memory on the router, measured
  and published in [docs/limits.md](docs/limits.md), and a rate or buffer set
  higher than a board can afford is a configuration, not an attack.
