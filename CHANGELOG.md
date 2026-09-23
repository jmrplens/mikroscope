# Changelog

Notable changes per release. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the versions
follow [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- **`doctor` reads what the running agent sees.** After the preflight checks,
  standalone `doctor` pulls the agent's ring once from this host and names four
  faults RouterOS's own tools do not show: a layer-2 loop (three or more frames
  back in on a port with the bridge's own source address), STP churn (a port
  moved to learning at least three more times than it was let forward), a link
  flap (two or more link-downs on one port) and softnet drops. Counts of events
  a healthy router does not produce, not thresholds tuned to one device: the
  loop on the reference RB5009 repeated every 2.0 s, and its 30 days of data
  left learning minus forwarding at 0 on every healthy link-up. An agent that
  does not answer within 3 s is said so and skipped. Findings never change the
  exit status.
- **Two warnings, a new `WARN` severity.** With `--remote-image`, doctor warns
  when a `/container/config` username is set and the pull goes anywhere but
  Docker Hub: RouterOS presents that one device-wide credential to every
  registry, and a Docker Hub account sent to GHCR ends the pull in `auth error`.
  And it warns when an install of the same `--name` is published on the LAN
  with no `TOKEN` in its environment. Both were reproduced on the reference
  RB5009 (RouterOS 7.24.4, 2026-09-23): the first read-only, the second with a
  throwaway install that was exposed, upgraded without a token and removed.
  Doctor reads whether a username is set and how many `TOKEN` entries exist,
  never a value. `WARN` changes neither doctor's exit status nor whether
  `install` proceeds.

- **An alert for a bridge port the bridge has stopped delivering to.**
  `mikroscope-bridge-port-dark` fires when a bridge port has received packets
  for ten minutes while the bridge sent it neither a unicast nor a broadcast
  frame: STP holding it discarding, or every host behind it learned on another
  path, with RouterOS showing the port running and error-free. Backtested over
  the reference store, 2026-09-19 11:13 to 2026-09-23 22:44 UTC, eight bridge
  ports in 10-minute bins: it marks sfp-sfpplus1 in 204 bins and ether2 in 376,
  the two phases of that week's layer-2 loop, and no other port before or
  after. For most of the first phase the own-address rule pointed at ether2,
  the wrong port. Broadcast as well as unicast, because a neighbor with no
  clients can leave tx-unicast at zero on a healthy port. It will fire on an
  RSTP alternate port and on `broadcast-flood=no` or `horizon`; the Prometheus
  form was checked for syntax only, since that store holds no API-tier history.

### Fixed

- **`doctor --disk` with a registry host passed the disk check on any answer.**
  Both optional checks read the last line of the batch, so the disk check read
  the registry-url line. Every answer is now looked up by name.

## [1.1.0] - 2026-09-22

### Added

- **A collector image, and two stacks that use it.** `ghcr.io/jmrplens/mikroscope`
  and `jmrplens/mikroscope` on Docker Hub, linux/amd64 and linux/arm64, beside
  the agent image that was already published. `deploy/` has a compose file for
  InfluxDB 3 with Grafana and another for Prometheus with Grafana, each a
  collector and somewhere for it to write, with the dashboard already in the
  Grafana beside it.

  Not `FROM scratch` like the agent: the collector reaches stores and a Grafana
  that are ordinarily behind TLS, and a static binary with no CA bundle fails
  every one of them with `x509: certificate signed by unknown authority`. It
  runs `nonroot` on distroless/static, and the deployment verbs are
  deliberately not usable from it — `ssh` and `scp` are not in the image,
  because a container that could reconfigure a router is a larger thing to hand
  someone than one that reads from it.

  **The collector runs on the host's network in both files, and that is not a
  shortcut.** The agent answers on a /30 veth inside the router and the route
  to it belongs to the host, so a container on a bridge network has no way
  there. `deploy/README.md` says so, gives the one-line check
  (`curl http://172.30.10.2:9123/healthz` on the host), and documents the relay
  as the other way in for a host that has no route.

- **shellcheck and hadolint in CI**, and `make shellcheck` in `make analyze`.
  actionlint already ran shellcheck over the `run:` blocks of the workflows;
  what was never linted was the scripts those blocks call and `install.sh`,
  which is the first command the README gives and so the one piece of shell
  most readers will execute. Both were clean when the check was added, which is
  the outcome worth having and not one worth assuming.

- **The README shows what it draws.** Two dashboard sections, the overview and
  the receive path, from the captures the site already generated; and the five
  badges that were missing, of which four report something the release already
  publishes.

- **`forward --grafana`: the collector sets Grafana up itself.** Point it at a
  Grafana and it reconciles one datasource and one dashboard per store it
  writes to, once, at start, before the first sample. It removes the step
  `dashboards import` could never remove — building the datasource by hand,
  and getting right the three settings a person gets wrong.

  - **It is off unless asked**, and refuses without `GRAFANA_TOKEN`: some
    Grafanas accept an anonymous request, and one that did would write as
    whoever the server thinks is asking.
  - **A failure is a warning, not a refusal to start.** Refusing would trade
    the samples of the hour spent not running, which cannot be recovered, for
    a dashboard published on the next restart, which can.
  - **Nothing is ever deleted.** A leftover under a uid nothing writes to any
    more is left where it is: it may be the copy somebody is looking at.
  - **Two of the five sinks can describe their own datasource**, because their
    write address is the address Grafana queries: `--influx` and `--elastic`.
    `--prom` is scraped, `--sql` writes to a file and `--graphite` speaks the
    ingest port, so those three are adopted through `--grafana-datasource-uid`
    or not published, and say so by name.
  - `--grafana-dry-run` prints what it would write and stops before
    collecting, so it is answerable with no router in front of it.

  The InfluxDB datasource it builds carries three things that are easy to miss
  by hand, and all three were missed here first: the token in **both** `token`
  and `httpHeaderValue1`, because the plugin reads one on the FlightSQL path
  and the other on the HTTP path; `insecureGrpc` following the URL's scheme,
  without which a plain-HTTP store answers every panel `tls: first record does
  not look like a TLS handshake` while the store itself is fine (measured
  2026-09-19 by creating the datasource without it); and the database in
  `jsonData.dbName` rather than the top-level `database` field, which this
  plugin ignores. Verified end to end against the live Grafana: folder and
  datasource created, and `dashboards check` against the datasource it built
  returned rows for every panel but the two the device had nothing to say
  about in the window.

- **`mikroscope-egress-queue-drops`**, the twelfth alert rule and the first
  whose counter is *supposed* to move. Dropping is how a full queue tells a
  sender to slow down, so "any drop" is not a fault on any device, and a
  packets-per-second threshold would be a number the device did not publish.
  It keeps the zero threshold and puts the judgement in the duration instead:
  a **one-minute** window with a **ten-minute** pending period, so it takes ten
  consecutive minutes of dropping to fire and no single burst can do it,
  however large. On the reference RB5009 a 1 GbE port lost 3 337 packets in
  six one-second bursts over 6.5 h, peaking at 436 packets/s (2026-09-19):
  real, visible on the egress queue panel, and correctly not an alert.

  The five-minute window every other counter rule uses would have fired on
  that: with a one-minute evaluation interval a single burst keeps the query
  non-zero for the five evaluations that still see it, which is exactly the
  pending period. The file's own note about thresholds now carries this as the
  shape to copy for anything whose healthy reading is not exactly zero.

  `mikroscope-port-errors` also reaches the alerts page, which 1.0.10 added
  the rule without.

- **`uninstall --targets`: take away what this put anywhere.** The verb has
  meant "the objects `install` created on the router" since 1.0.0 and still
  does when given nothing else — widening what an existing destructive verb
  does by default is not a thing to do to somebody who has it in a script.
  `--targets` opens the rest: `dashboard` for what `forward --grafana`
  published, `data` for what the sinks wrote, `all` for the three.

  **Nothing is removed without `--yes`**, and the default is the list. The
  alternative is one mistyped command that empties a store, and unlike the
  router objects — which `install` puts back — a dropped table is a dropped
  table.

  **The tables are asked of the store, never compiled in.** A list inside the
  binary would be the measurements this version writes, and the ones worth
  removing are exactly the ones nobody writes any more: what an earlier
  version collected, or a source switched off since. Everything under the
  `mikroscope_` prefix is claimed and nothing else is ever touched.

  Three sinks store nothing this can remove and each says so by name rather
  than being silently absent — `--prom` is scraped, `--graphite` offers no
  delete, `--sql` writes a file whose rows live wherever they were loaded. An
  adopted datasource is left alone, as publishing leaves it alone. One item
  that will not go is reported and the rest still go, because stopping at the
  first would leave it half done with no list of what is left.

  Verified in the docker suite on 2026-09-20 against the live stores, with the
  assertion that matters most: a table **this project did not write**, sitting
  in the same database and the same schema, is neither listed nor removed. A
  run without `--yes` removes nothing; a run with it empties both stores; a
  second run says they are already empty.

  **What that run found about InfluxDB 3:** it deletes a table by *renaming*
  it, leaving the entry in `information_schema` as
  `mikroscope_cpu-20260919T225605`. Listed again next time, deleted again
  successfully — the server accepts a delete of a name it has already retired
  — and the list never empties. Those are filtered out, and the delete asks
  for `hard_delete_at=now`.

- **Five datasources of five, by two routes.** `forward --grafana` derived two
  in 1.1.0's first cut; it derives three now and is told the other two.

  - **`--postgres` derives its own**, from the connection string it dials
    with, parsed by **pgx's own parser** rather than a second one written
    here: a parser of this project's own would disagree with the sink about
    what the flag says on exactly the inputs where being wrong matters. Host,
    port, database, user and `sslmode` all come out of it, in the URL form and
    the keyword form alike.
  - **`--prom` and `--graphite` are told the address** with
    `--grafana-datasource-url`, because neither can know it — one is scraped
    rather than written to, the other speaks the ingest port and not the web
    API. Told it, there is nothing else to derive: a Prometheus datasource is
    a URL, and so is a Graphite one. Before this they could only be adopted.
  - **`--sql` is the one that can never be described**, and it now says so by
    naming `--postgres`, the sink that can, instead of sending the reader to
    Grafana.

  Two decisions in the PostgreSQL one are worth stating. It copies **only a
  password the connection string itself carries**: pgx reads `PGPASSWORD`,
  `~/.pgpass` and the service file the way libpq does, which is what the sink
  wants, but a datasource is written into a Grafana other people can see, and a
  credential that came from the publishing machine rather than from the
  configuration is one nobody asked to put there — it says so and declines. And
  `sslmode` is read rather than guessed: libpq's default `prefer` has no
  Grafana equivalent, so a connection string that says `prefer`, `allow` or
  nothing gets `disable` **and a line saying so**, because guessing `require`
  would leave the other half of readers with a datasource that cannot connect
  at all. `--grafana-datasource-sslmode` decides it outright.

  Verified in the docker suite on 2026-09-20 the only way that means anything
  here: the collector created each of the five datasources against a real
  Grafana, and then `dashboards check` ran every panel's query **against the
  datasource the collector had built**. What goes wrong is never the JSON —
  Grafana stores a datasource happily and the query path then answers
  `flightsql: Unauthenticated` or `tls: first record does not look like a TLS
  handshake`, which is how both of this project's InfluxDB traps presented.

- **`--postgres`: the SQL sink's other half, down a connection.** The
  collector can write to a PostgreSQL that is running instead of to a file
  somebody replays later. `--sql` and `--postgres` are ONE RENDERER with two
  transports — internal/sinks/postgres.go sends the statements
  internal/sinks/sql.go produced — because the dashboard this project
  generates for the postgres store has to be true of a deployment that used
  either, and a second renderer would pass a row count and still drift a
  column.

  The file is not deprecated and is still what you want when the database is
  unreachable, when the load happens later or under review, or when the reader
  is not PostgreSQL at all. Both can run at once.

  Two things the connection does that a file cannot. A batch goes inside a
  transaction, so it lands whole or not at all and a retry cannot leave half
  an event behind. And `SET standard_conforming_strings = on` stops being
  advice: the header can only ASK a file's reader for it, but a connection can
  be asked back, so it is — a server that answers `off` is refused with the
  reason rather than written to, because a kernel message ending in a
  backslash would escape its own closing quote and everything after it would
  be parsed as string content.

  Verified against a real server on 2026-09-20: the docker suite runs the
  sweep with both sinks at once, the script into one database and the
  connection into another, and asks PostgreSQL whether the two hold the same
  thing. **34 tables matched byte for byte** — every column of every row,
  hashed per row and summed — with an identical information_schema for the
  whole schema. pgx reaches the collector binary only; the agent still links
  procfs, sample, agent and the standard library, and its arm64 image is
  unchanged at 6.5 MiB.

- **`--influx-db`.** `--influx` is the server now and the write URL is
  assembled from the fields: `--influx http://influx:8181 --influx-db
  mikroscope`. A write URL is the sink's shape and the wrong shape for
  everything else — Grafana wants the server and the database apart and will
  not take a write path at all.

  A full write URL is still taken **verbatim**: every 1.0.x deployment has one
  in `MIKROSCOPE_INFLUX_URL`, including this project's own systemd unit.
  Its database is read back out of that URL and never from `--influx-db`, so
  a datasource cannot end up pointed at a database nothing fills; a URL this
  cannot take apart, a v2 `/api/v2/write` for instance, writes as well as ever
  and simply cannot describe a datasource, which `forward --grafana` says
  rather than building one that answers nothing.

- **A tile, an alert and a playbook for a port losing frames.** The reference
  RB5009's `ether1` had been dropping 0.5 % of the packets
  the NAS sent for days — the data was in the store the whole time and nothing
  on the dashboard said so.

  - **"Port errors in the window"** joins the Overview tiles: every typed MAC
    error on every port, summed, **green at 0 and red above it**. Verified
    against the live deployment in both states — 21.4 k over a window that
    contains the fault, 0 over one that does not.
  - **`mikroscope-port-errors`** fires when any port counts a typed error for
    five minutes running. It is the twelfth rule and the first that reads the
    API tier's per-port counters.
  - **[A port losing frames]** is the eighth playbook, in both languages: how
    to get from the red tile to which port and which error, and then to the
    question the fix hangs on — is the port busy, or is the sender bursting?
    The test is numeric: compare the receive volume in the intervals that
    overflowed against the link's capacity. On the reference device that was
    8.96 Mbit/s on a 2.5 Gbit/s link, 0.36 % of it, which rules out load.

    It records what did NOT work as carefully as what did: a smaller MTU cut
    the worst bursts by 91 % and did not change the frequency at all, and
    Ethernet flow control never fired — 41 minutes with pause negotiated, 5 578
    overflows, `rx-pause` and `tx-pause` both still 0. What worked was pacing
    the sender below what the slowest destination drains.

[A port losing frames]: https://jmrp.io/docs/mikroscope/playbooks/port-errors/

### Fixed

- **`uninstall --expose` removed neither firewall rule, and said it had.** Both
  expose selectors carried `protocol=tcp` unquoted, and a RouterOS `find` reads
  a bare word as a variable name — an unset variable is the empty value, so the
  selector matched nothing. Measured on the reference RB5009 (7.24.4,
  2026-09-21): over the same 15 dstnat rules, `find chain=dstnat protocol=tcp`
  returned 0 and `find chain=dstnat protocol="tcp"` returned 10.

  The same string is the existence check, the ownership check and the removal,
  so all three agreed with each other and disagreed with the router:
  `uninstall --expose` left both rules and then printed `verified: nothing
  mikroscope created remains on the router` over a live dst-nat pointing at a
  container address that no longer existed, and `install` could not see its own
  rule, so installing twice left two copies. Creation was never affected —
  `add protocol=tcp` takes a bare word — which is why the rules appeared
  correctly and were invisible only to their own queries.

  Both selectors quote the value now, and `TestFindSelectorsQuoteEveryValue`
  fails on any `find` in the plan that compares against a bare word. The fixed
  binary removed the two rules the unfixed one had left behind.

- **Seven of the twelve InfluxDB alert rules could never fire.** The InfluxDB
  sink writes its counters unsigned, so `coalesce(sum(count), 0)` is
  `coalesce(UInt64, Int64)` — a pair Grafana's InfluxDB plugin cannot map into
  a frame. It answers **HTTP 200 with no frames and no error**, Grafana reads
  an empty result as NoData, and every rule but the silent-agent one declares
  `noDataState: OK`. The rule sits at OK forever while the condition it watches
  is true.

  Found on 2026-09-21 by loading the provisioning file into Grafana 13.2.1
  against the live store and watching all twelve evaluate — which is a thing
  nothing had done before, and the reason this shipped. `mikroscope-l2-loop`
  was one of the seven, dead *while the `own-address` loop signature it exists
  to catch was running*: the same SQL over `/api/v3/query_sql` returned 109 at
  that moment. Every `coalesce()` over an aggregate now carries `::BIGINT`;
  afterwards all twelve returned a value and that rule went to Alerting on the
  live signature. `TestCoalesceIsCastInAlertSQL` fails if a new rule omits the
  cast. It is the same fault `TestGreatestIsCastForTheInfluxPlugin` already
  pinned for panels, and worse, because a panel fails loudly with a 500 and
  this returns success and nothing.

- **The InfluxDB conntrack rule named a column no InfluxDB store holds.** It
  read `limit_objs`, the SQL sink's name, while the InfluxDB sink writes the
  slab ceiling as `limit`; the rule failed at planning on every InfluxDB store.
  The documentation had predicted this defect from the code before anything
  confirmed it. The rule reads `"limit"` now, and the PostgreSQL translation
  turns it back into `limit_objs`.

- **A second `--remote-image` install on one router refused itself.** The
  container was identified by its registry reference, which is the same string
  for every mikroscope install anywhere, so a second install under its own
  `--name`, `--veth` and `--subnet` found the first and refused with "exists on
  the router and was not created by mikroscope" — which is what had just been
  done. It is identified by its veth now. Measured on the reference RB5009 on
  2026-09-21 while testing the GHCR route beside the running install.

- **A collector writing only to `--postgres` published nothing.** The store
  list that decides which dashboards to publish still only knew `--sql`, so a
  deployment using the connecting sink alone was told "there is nothing to
  publish". Found by the end-to-end run that publishes all five, not by any
  unit test: the list was right about the four stores its own test covered.

- **One event, ten timestamps.** Every sink called `time.Now()` while
  rendering the events that have no clock of their own — a gap, the device
  facts, the sampler's counters — so one gap reached ten stores with ten
  different timestamps. Microseconds apart, which nobody would notice, and
  different, which makes two stores disagree about when it happened.

  The Postgres sink is what brought it out, because it and the SQL sink are
  one renderer and their rows are comparable byte for byte: nine of the
  thirty-four tables did not compare, and all nine were the clock-less ones.
  The forwarder now reads the clock once per event into `sinks.Event.At` and
  every sink uses it.

- **The interrupt panel that drew nothing at all.** "Which core takes each
  interrupt" carried `FillOpacity: 0` on a stacked bar chart, and a bar with
  no fill is a bar that is not there. On the reference device (RB5009,
  RouterOS 7.24.2, 2026-09-19) it returned 21 series totalling 4 300
  interrupts/s, auto-scaled its axis to 7 K c/s to fit them, printed all 21 in
  the legend — and drew an empty plot. Its own description says the reading is
  "the stack redistributing while its total stays flat", which it had never
  been able to show.

  An empty plot under a full legend reads as a quiet device rather than as a
  broken panel, which is the worst shape a dashboard defect can take: nothing
  errors, nothing reports no data, and the panel looks like good news. A test
  now states the rule for every bar panel in both stores.

- **The drops panel, split in two and given a scale.** "Interface drops — rx,
  tx and tx-queue" drew one state-timeline lane per interface per counter:
  forty-eight lanes on sixteen interfaces, of which eighteen could never carry
  anything, because RouterOS returns `rx_drops` and `tx_drops` for the seven
  virtual interfaces and for **none** of the nine physical ports (measured
  2026-09-19 over 7 198 samples each). A blank lane and a zero lane looked the
  same, so the panel could not say whether a port reported zero or did not
  report.

  And the colour was binary, so the quiet fault was the loud one: over the same
  window `wg_devices` dropped exactly one packet in each of 172 separate
  seconds and `ether4` dropped 3 337 in six one-second bursts peaking at 436/s.
  The trickle painted the louder lane.

  Two bar panels instead — **"Egress queue drops — the router's own transmit
  queue"**, which the old panel's own description called the counter to watch,
  and **"Packets the interface itself dropped — rx and tx"**, whose no-value
  text says what a missing interface means here, because it does not mean
  zero. Each is filtered to the interfaces that dropped anything in the window
  and each carries the number. Neither defines thresholds: `colorFor` would
  switch them to `color.mode: "thresholds"` and paint every series by value,
  leaving a legend that cannot be matched to a bar. The zero / non-zero
  judgement stays in the stat tile and the alert rule, where one number can
  carry it.

- **Three interface panels were unusable, and `dashboards check` called all
  three "ok".** Found by looking at the reference deployment rather than at the
  query results — the check verifies that a query returns rows, not that the
  rows are legible or the numbers sane.

  - **"Port errors per bin" drew 187 rows** — every interface against every
    typed error counter — in a nine-unit panel, which renders as a grey smear
    of overlapping labels. It now lists only the pairs that actually had an
    error in the window, and says `no port errors in this window` when none
    did. On the reference RB5009 that is one row, `ether1 rx overflow`, which
    the smear had made unreadable.
  - **"Where a port's receive bytes went" peaked at 1.5 EB/s.** The slow-path
    term is `driver_rx_byte - fp_rx_byte`, both `UInt64`, and the two counters
    come from the same command without being perfectly consistent: on a handful
    of samples the second exceeds the first, the subtraction wraps to ~1.8e19,
    and the real traffic — single-digit MB/s — is flattened to an invisible
    line. Both subtractions are now signed and floored at zero.
  - **"Interface drops" said `No data`** where the truth is that there were no
    drops: the router reports the loss keys for `bridge` alone, and that one
    reads 0. It says `no drops`.

- **A `greatest()` over an aggregate makes Grafana's InfluxDB plugin answer
  500** — `An error occurred within the plugin` — while the store answers the
  same SQL correctly over `/api/v3/query_sql`. The panel then renders its
  no-value text, so a broken query looks like a quiet device: the port-error
  panel claimed "no port errors" while `ether1` was overflowing. Casting the
  result (`::DOUBLE`) fixes it, and a test now fails the build for any
  uncast one.

- **`upgrade --dry-run` wrote to the router.** The flag is documented as "print
  the plan and write nothing"; `upgrade` never read it. It printed no plan and
  fell through to the confirmation prompt, so the only thing standing between a
  dry run and a replaced container was answering `n` — and **`upgrade --dry-run
  --yes` replaced it outright**, on a live router, while promising it would not.
  Found on 2026-09-19 while upgrading the reference RB5009's agent to 1.0.9:
  the dry run printed nothing but the prompt, which is what gave it away.

  `upgrade` now prints its own plan and `--dry-run` returns before the runner
  is built, so a dry run opens no connection at all. The plan is the container
  step alone — `install`'s listing names the veth, the router address and the
  two list memberships, which an upgrade does not touch, and printing them
  would promise writes that never come.

### Documentation

- **The bilingual site was audited page by page against the code, and the first
  three passes of the result are applied.** Fifty-three English pages and their
  Spanish twins were read against the source; 234 findings were proposed and 206
  survived an adversarial refutation pass. The documentation had been updated
  release by release rather than by sweep, so every change since 1.0.3 left a
  trail of pages behind. Fixed here:

  - **The agent's `/metrics`.** 1.0.5 removed the exposition from the agent
    entirely, and sixteen page pairs still attributed one to it — telling a
    reader to scrape the agent, to take "two reads of `/metrics` 60 s apart" on
    it, or reasoning from a two-exposition world that no longer exists. Among
    them `reference/metrics.mdx` cited `internal/agent/metrics.go`, a file
    renamed to `internal/expo/expo.go`, in a page whose own voice is "no claim
    without its evidence".
  - **The ring's default.** 1.0.6 made it 60 s; ten page pairs still said 300,
    including the conditions line of `about/status.mdx`, which quoted figures
    measured with a 60 s ring under a sentence promising 300.
  - **The line size.** `ApproxLineBytes` has been 3 456 B since 2026-09-17;
    `site/src/data/measurements.ts` still carried the 2 439 B of 2026-09-12, so
    a dozen pages rendered the old number from the data file rather than from
    stale prose. The data now carries the measured line (3 230 B) and the
    charged size class (3 456 B) as separate ids, because they stopped being the
    same number.
  - **Provenance.** Every `run.*` measurement was attributed to campaign
    `rates-2026-09-15` while the table it reads is the six-run campaign of
    2026-09-18, and the landing page cited the superseded campaign while its own
    data file used the current one.
  - **`MEM_LIMIT_MB`** is derived from the ring, not a flat 40, and `BUFFER_S`
    defaults to 60 — both wrong in `site/src/data/envlist.ts`, which feeds three
    pages in two languages.
  - **InfluxDB 3 Enterprise is now measured.** `sinks/influxdb.mdx` said no
    write to it was recorded; on 2026-09-19 the reference collector moved onto
    Enterprise 3.11.4 and wrote 44 820 rows across 36 tables in nineteen
    minutes, 0 dropped and 0 errors.

- **The audit's remaining passes.** The reference tier, the five-dashboards
  sweep and the 1.0.9/1.0.10 strays, measured rather than assumed at every step:

  - **The SQL sink declares 43 tables, not 32.** `reference/measurements.mdx`
    carried seven "not written" rows for data that has had a table since 1.0.3 —
    CPU frequency, per-CPU interrupts and softirqs, vmstat events and levels, the
    PMU, `mikroscope_sample` — and described `mikroscope_mem` as five columns
    when it has nineteen. `sinks/other.mdx` asserted eight absences of which
    exactly one, the kernel-log count table, is real. The header for all 43
    tables is **10 482 B**, not 7 757: rendered by the sink's own `header()`
    rather than estimated.
  - **Five dashboards, eight generated files, three alert files.** Pages said
    two, four and two. `reference/testing.mdx` said the suite imports "both"
    dashboards; it loops over five. `about/status.mdx` quoted 209 PostgreSQL
    queries and 171 InfluxDB panels; counted from the committed JSON they are
    **216** and **175**.
  - **`AlertRules.astro` printed "InfluxDB only" for rules all three stores
    carry.** `storesText` handled one store or two, so the eight rules in every
    alert file fell through to the InfluxDB branch — on a page that shows their
    PromQL directly underneath. The component now names the stores it was given,
    and `alertUids` unions all three files instead of two.
  - **The probe covers InfluxDB and Prometheus only.** `--store postgres`,
    `graphite` or `elasticsearch` always fails the probe, always warns and always
    ships the compiled defaults — the same as `--no-probe`. Neither twin said so.
  - **The PostgreSQL dashboard drops 15 panels silently**, not ten queries "that
    say so", and the kernel-log panels are among them.
  - **`api tier disabled` no longer exists**; 1.0.9 replaced it with a tier that
    keeps retrying. Four pages still told operators to expect it.
  - **`sinks/detections.mdx` carried two Asides built on a premise 1.0.4
    removed** — that a running collector never sees a restarted agent. It does,
    within a minute, and the reference device exercised it for real on
    2026-09-19.

- **The twin-drift list and the factual low-severity findings.** Where the two
  languages disagreed, each needed both files opened: a sentence the 1.0.10
  commit deleted in English and left standing in Spanish, "las cinco
  ejecuciones" against "the six runs", two table rows present in English and
  missing in Spanish (`/system/resource/cpu/print` and `--api-user`), the
  project's "sub-second" claim dropped from the Spanish head title, and a
  half-finished 1.0.5 edit that left a different broken plural in each language.

  Among the small ones, four were plainly wrong rather than merely dated:
  `/proc/buddyinfo` was described as a NAND wear counter (it is the page
  allocator's free lists), `--hz 10` is not a flag (`--rate`), the token-guarded
  endpoint list still named `/metrics` and omitted `GET /sampler`, and the
  InfluxDB page listed four of the five fields `notCounters` drops.

- **The rest of the audit, and three numbers put behind assertions so they
  cannot drift again.** What kept going wrong was not prose but arithmetic
  written by hand:

  - **The relay cap is 13, not 18.** It is
    `RelayMax x 100 / (line x headroom)`, so it fell when the line grew to
    3 456 B in 1.0.5 and the shipped binary has printed 13 ever since — while
    six pages went on saying 18, with "about 46 kB" and "36 samples a second"
    behind it. It now renders from `measurements.ts`, which recomputes it from
    the same line size and **throws** if the two disagree.
  - **Three slipped percentages were a consistent x0.75 out**: 5/30 000 is
    0.017 %, not 0.012; 4/15 000 is 0.027 %, not 0.020; 178/29 996 is 0.593 %,
    not 0.444. The campaign's own denominators are now in the data, and a
    build-time assertion divides them.
  - **`about/status.mdx`, the page whose job is the honest state, was the least
    current page on the site**: "the current release is v1.0.0", images pinned
    at `:1.0.0`, and "above the budget" on both axes when the install default
    has been inside the memory one since 1.0.6.
  - **The install pages named a release that does not exist.** Bumping `VERSION`
    to 1.0.10 took them with it; v1.0.10 is not tagged. They name v1.0.9, the
    latest published release, and should move at release time rather than at
    version-bump time.
  - **`install/routes.mdx` rendered two whole sections inside its "See also"
    nav**, headings shrunk to `h4`, because the wrapper opened 110 lines early.
  - **`reference/environment.mdx` documented nine environment variables that
    exist nowhere in the repository** — `OPERATOR_HOST_IP` and the whole
    `HEXS_*` block. Section deleted.
  - **The agent cannot read port counters**, which `cost/index.mdx` listed among
    the sources it reads; **`mikroscope-agent-arm.tar` has not existed since
    1.0.2**, and naming it sends an armv5 board the armv7 image; and the squeeze
    figure was attributed to a 3 476-sample run when it comes from 3 738 704
    samples over 24 h, leaving one campaign backing no measurement at all.

- **The audit's last pass: the tables that promised completeness, and the
  landing page's grammar.**

  - **The landing page rendered "the one sinks the collector forwarded to"** —
    and "los uno destinos" in Spanish — because a spelled count met a fixed
    plural the day the campaign came down to one sink. The sinks are named now,
    not counted.
  - **`sinks/influxdb.mdx` promised "every measurement it writes" and omitted
    the four 1.0.5 added**: `mikroscope_sampler`, `mikroscope_trigger_count`,
    `mikroscope_trigger_suppressed`, `mikroscope_capture_refused`. The OTLP,
    Graphite, Elasticsearch and file-sink listings had the same gap.
  - **The site header inlines `favicon-inline.svg`, not `mark-inline.svg`** —
    the brand page named the wrong drawing for its own chrome.
  - **`make build` builds the CLI alone**; the page said it leaves both
    binaries, and a bare `bin/mikroscope-agent` is a path this build never
    produces.
  - A `/snapshot` of 600 lines is **~1.9 MB**, not 1.5: the same ×0.75 the line
    size left behind, in three pages.
  - A sink's drop count is **not exported as a metric**, no family exists for
    it; a Graphite defect listed as *found and not fixed* was fixed; the
    conntrack occupancy quoted 0.63 % where the recorded reading gives 0.65 %;
    and the squeeze figure quoted a hand-typed 2 % beside a `<Measured>` that
    says 1.2 %.
  - `about/lineage.mdx` credited the wrong project for `.golangci.yml` — the
    file's own header names two others — and both it and
    `internal/rosapi/README.md` said "nothing modified" when 1.0.1 added one
    Windows-only test skip.

  The audit is closed.

## [1.0.9]

### Fixed

- **The API tier now reconnects.** It holds one persistent RouterOS API
  connection, and nothing ever reopened it. On the reference RB5009 a RouterOS
  upgrade on 2026-09-19 rebooted the router at 00:43:30 CEST; the kernel tier
  resynced at 00:45:03 and carried on, and the API tier wrote to the dead
  socket for the next **7 h 24 min** — 10 800 failures an hour, one per
  command — until the collector was restarted by hand at 08:07:49. Every panel
  the API feeds was blank for that window: interface throughput and packet
  rate, per-port counters, RouterOS cpu-load, and "Reboots in the window",
  which reads `uptime_s` and so could not count the very reboot that broke it.
  A transport failure now reopens the connection, at most once every 5 s, and
  the round is retried on the new one. A `!trap` does not: that is a live
  router refusing a command, and repeating it would only spend its CPU. A
  `!fatal` does, because that is the word RouterOS sends as it closes the
  session. The inventory is re-read after a reconnection, since an upgrade is
  exactly when an interface can change its name, type or bridge.

  Verified against the reference RB5009 on 2026-09-19 without touching the
  router: a collector polling all 16 interfaces at 1 Hz had its API socket
  destroyed from the host (`ss -K`), which is what the router's side of a
  reboot looks like to it. It reopened the connection and retried inside the
  same round — **0 failed commands, 0 dropped rounds, 16 interfaces in every
  one of the 108 seconds** either side of the kill. Not one sample was lost.

- **A collector that starts while the router is down no longer gives up on the
  API for the life of the process.** The first dial failing is a warning now,
  not a disabled tier; the reader connects on the first round the router
  answers. With `Restart=always` in the unit, a reboot could otherwise leave a
  restarted collector permanently without an API tier.

- **A kernel-only run no longer panics one hour in.** `forward` arms the API
  ticker at an hour and resets it to the real cadence only when there is a
  tier, but it never stopped it — so with `--api-every 0`, or with no API
  credentials, the first tick dereferenced a nil reader. Any run past the hour
  mark crashed. Found by reading the loop while fixing the reconnection, not by
  hitting it: the reference deployment has always run the API tier.

- **The report no longer hides an API outage.** `api` counts rounds
  *attempted*, so through those 7 h 24 min the summary line read a healthy,
  growing `82 610 api`. It now carries `api: N failed round(s), N
  reconnect(s)` when either is nonzero, and nothing when both are zero.

- **The failure is logged once, not once per command per second.** The outage
  put **44 257 identical lines** into three hours of journal, which buried the
  first one — the only one that said what happened. The first failed round is
  logged, the rounds after it are silent, and the recovery is logged with the
  count of what it closed.

## [1.0.8]

### Changed

- **The rate campaign was re-run at the shipped configuration, and 20 Hz was
  added.** The table on [the rate ceiling] described a product the project no
  longer ships: its rows came from 2026-09-15 with a 300 s ring at 10 Hz,
  hand-set memory limits, and a container cap raised to 96M at 50 Hz and 128M
  at 100. Six windows of 300 s on 2026-09-18, every one at the defaults — 60 s
  ring, derived limit, the 64M cap — with the collector forwarding to
  InfluxDB 3:

  | rate                 | limit | RSS      | of one core | slipped         |
  | -------------------- | ----- | -------- | ----------- | --------------- |
  | 10 Hz                | 16    | 13.2 MiB | 2.69 %      | 0 of 3 000      |
  | 20 Hz                | 16    | 15.4 MiB | 4.61 %      | 0 of 6 000      |
  | 50 Hz                | 25    | 23.3 MiB | 9.63 %      | 0 of 14 999     |
  | 100 Hz               | 48    | 45.7 MiB | 16.83 %     | 5 (0.017 %)     |
  | 50 Hz `FLOOR_HZ`     | 25    | 25.1 MiB | 22.56 %     | 4 (0.027 %)     |
  | 100 Hz `FLOOR_HZ`    | 48    | 49.5 MiB | 42.70 %     | 178 (0.593 %)   |

  **Zero collector gaps in all six.** Against the old campaign that is 38 to
  59 % less memory per row with the CPU unmoved, and the whole range — up to
  every source on every tick at 100 Hz — now fits the default 64M container
  cap, which the old campaign could not do.

- **The agent is inside the memory budget for the first time.** The project's
  own budget is ≤ 2 % of one core and ≤ 16 MiB; the install default now
  measures 2.69 % and **13.2 MiB**. It was over on both until the 60 s ring and
  the derived limit. The build-time assertion that guarded the sentence "above
  the budget" is what caught the prose the day it stopped being true — it threw,
  named the two files to rewrite, and they were rewritten.

[the rate ceiling]: https://jmrp.io/docs/mikroscope/cost/rate-ceiling/

## [1.0.7]

The release 1.0.6 should have been. **There is no 1.0.6 release**: the tag
exists and points at a commit that is in this one, but its release run failed
and releases here are immutable, so the tag was left where it was and the
artefacts come out under this number instead.

### Fixed

- **Two tests in the containerised suite still read the agent's `/metrics`**,
  which 1.0.5 removed. That suite is skipped on a pull request and runs on a
  tag, so every check on the three pull requests that made 1.0.5 and 1.0.6 was
  green and the release run was what found it.
  `TestPrometheusDashboardMetricsExist` now checks the dashboard's names
  against the collector's exposition alone — the stronger statement, and the
  one the other end-to-end suite already made — and the sweep no longer
  fetches an exposition nothing serves.
- **A race that the removed fetch had been hiding.** `TestPrometheus` compares
  every unlabeled series Prometheus stored against the last body the test read,
  on the grounds that the exporter's counters only rise and the read came
  last; the agent fetch was the delay that made the read come last.
  `mikroscope_uptime_seconds` is not one of those counters — it follows the
  clock — so a scrape taken after the read legitimately carries a larger
  value. It is excluded by name, with the reason.

### Changed

- **The stores suite runs on every pull request.** It ran weekly, on dispatch
  and on the release gate, on the grounds that nine containers are a lot to
  ask of a pull request; the cost of that was one failed release. MEASURED on
  the pull request that introduced this: **2 min 22 s**, containers included,
  in parallel with the two end-to-end jobs that take about a minute each.

## [1.0.6]

### Changed

- **The ring holds 60 s by default, not 300.** What it buys is how long the
  collector may be absent before samples are lost — it is not a window anybody
  reads, because the collector drains it twice a second — and the 300 was in
  the code with no recorded reason. MEASURED on the reference deployment over
  24 hours on 2026-09-17: the largest interruption in delivery was **114.5 s**,
  and it was self-inflicted, a container swap plus the minute the collector
  takes to notice a restarted agent; in ordinary running the collector never
  falls behind, and `mikroscope_gap` has recorded nothing since the 50 Hz
  experiments of 2026-09-13.

  60 s covers a restart of either side on a LAN and costs **2.0 MiB of ring at
  10 Hz instead of 9.9**. Because the memory limit is derived from the ring, a
  default install now writes `MEM_LIMIT_MB=16` instead of 25. A deployment
  whose collector disappears for longer — a flaky link, a host that reboots
  slowly — raises it with `--buffer`, and the limit follows.

  On the reference device the whole change is **13 627 392 B of RSS against
  33 042 432 this morning, a 57 % cut, with the CPU unmoved** (2 740 µs a
  sample against 2 657) and 0 slipped ticks.

### Added

- **[The rate ceiling](https://jmrp.io/docs/mikroscope/cost/rate-ceiling/)
  gains the memory a rate costs**, with 10 Hz and 100 Hz measured side by side
  over windows of about 54 000 samples: 100 Hz holds a 19.78 MiB ring at 48 MiB
  of RSS and 18.1 % of one core, slipping 0.03 % of ticks — and the per-sample
  cost *falls* with the rate, because the level sources are read at their own
  floors rather than every tick. The guidance that comes out of it: the ring is
  the memory, and it is bought in buffer seconds. At 100 Hz a 20 s buffer gives
  the same relief as compressing the ring 4.7×, and compression was measured on
  this device at +1 331 µs a sample — at 100 Hz, +74 % CPU and six times the
  slipped ticks. The seconds are free; the compression is not.

## [1.0.5]

The agent stops being a Prometheus exporter, and the memory limit stops being
a number somebody picked. Everything it knows now leaves it
as data, through the collector, to whichever sinks the operator configured.

### Changed

- **The agent serves no `/metrics`.** Not a flag and not a 404 branch: the
  exposition moved out of the agent's import graph into `internal/expo`, which
  only the collector's Prometheus sink links, and the sampler no longer folds
  every tick into cumulative counters, histograms and trailing windows. The
  arm64 agent is 131 072 bytes smaller (1.9 %). On the reference RB5009, an
  agent built this way measured **29.2 MiB of container memory against 30.6**
  and **4.5 MiB less RSS**, over two windows of exactly 12 000 samples on
  2026-09-17; the CPU difference was +76 µs per sample against a standard
  deviation of 905 µs, which is the router's own load moving between windows
  and not the change.
- **What only a sampler can know travels instead of being scraped.** The wake
  latency and the read duration of every tick — the two timings that make a
  tick a smear rather than an instant — ride in the sample's `self` block as
  `wake_ns` and `read_ns`. `GET /sampler` answers what is not per-tick: ticks
  taken, ticks slipped, and what the trigger evaluator has fired, suppressed,
  refused and is holding, with every configured condition present at 0 from
  the first read. The collector reads it at start and on its one-minute health
  cadence and fans it out like any other event.
- **One scrape job, not two.** The collector's exposition now carries every
  family the Prometheus dashboard asks for, including
  `mikroscope_slipped_total`, the three `mikroscope_tick_*` histograms and the
  trigger and capture counters. The end-to-end suite used to scrape the agent
  and the collector; it now asserts against the collector alone, which is a
  stronger statement. There is no keep list to maintain and nothing left to
  double-count.

- **The agent's memory limit is derived from its ring**, not fixed at 40 MiB:
  rate × buffer × the line size, times 2.5, floored at 16 MiB and capped at
  three quarters of the container's `memory-max`. A default install now writes
  `MEM_LIMIT_MB=25` instead of 40. A fixed number cannot be right for every
  rate — the same 40 left 8 MiB unused at 10 Hz and is below the ring itself
  at 50 Hz — and the factor is measured rather than chosen. On the reference
  RB5009 on 2026-09-17, four limits over four windows of ~12 000 samples:
  40 MiB gave 32.9 MiB of RSS at 2 657 µs a sample, **25 MiB gives about 26 at
  no measurable cost**, 21 MiB gives 23.5 at +22 %, and 18 MiB gives 20.4 at
  **+457 %** with a worst tick of 52 ms. 2.5× is the last comfortable factor
  and 2.0× — where the agent's own budget warning sits — is already past the
  knee.
- **`ApproxLineBytes` was 35 % low.** It said 2 560 B where the reference
  device's line is 3 230 B, served from the allocator's 3 456 B size class,
  which is what the heap is charged. That understatement is why the old fixed
  limit never bound: the budget check thought the ring was 7.3 MiB when it was
  9.9. Measured on 2026-09-17 and dated in the constant, with the warning that
  it moves with the board.

### Added

- **Every store sink carries the agent's own counters**, which is the point:
  `mikroscope_sampler`, `mikroscope_trigger_count`,
  `mikroscope_trigger_suppressed` and `mikroscope_capture_refused` on InfluxDB,
  four tables in SQL, a path per condition and reason on Graphite, a document
  on Elasticsearch, sums and gauges on OTLP, a line on stdout and in a
  recording. Loki deliberately writes nothing: a log stream is for what
  changed, and these are levels read every minute.
- **Four observer panels gain an InfluxDB form**, so the InfluxDB dashboard
  goes from 171 panels to 175: the tick interval (`dt_ns` itself, one row per
  tick and no buckets at all), the wake latency, the read duration and what
  the held captures pin. Until now they existed on Prometheus alone.

## [1.0.4]

One fix, found by deploying 1.0.3 on the reference device and watching what
happened next.

### Fixed

- **The collector stopped forwarding kernel samples after the agent
  restarted, and nothing said so.** The agent numbers its samples from 1 at
  every start, so an agent that restarts — an upgrade, a container restart, a
  reboot — has a newest sequence number far below the collector's cursor. The
  ring answers an empty batch to a request for samples after a number it will
  not reach for days, so the cursor never moved again: the kernel tier stopped
  for good while the API tier kept counting and every sink kept being written,
  which is what made it invisible.

  MEASURED on the reference deployment on 2026-09-17, upgrading the agent from
  the development build to 1.0.3: the last kernel sample forwarded was seq
  1 737 212 at 11:52, and a minute later the report still read `865 kernel …
  last seq 1737212` with the API count grown from 109 to 169 and the agent
  healthy at seq 571. Restarting the collector was the only thing that cleared
  it, because `Run` takes its cursor from the health read at start.

  The collector now notices on its next health read — once a minute, the only
  one the loop makes — logs the two sequence numbers, and resumes from the new
  ring's oldest sample, so what the agent took while nobody was collecting is
  picked up rather than skipped. The run report counts the restarts it saw.
  The minute between the restart and the health read is lost with the
  container, not by the collector; a health read per pull would ask the router
  for something twice a second to catch an event an operator causes.

## [1.0.3]

Five dashboards instead of one, the stores widened so the new ones have
something to read, and the fix for four panels that had been empty on the
reference deployment for a day without anybody noticing.

### Added

- **A dashboard per datasource type**, all five generated by
  `mikroscope dashboards gen` from the same panel list: InfluxDB 3 (171
  panels over 23 sections), PostgreSQL (156 over 21), Prometheus (133 over
  22), Graphite (41 over 14) and Elasticsearch (30 over 11). The three that
  can carry them get alert rules beside them. They are not the same
  dashboard five times and the pages say why: ten of the InfluxDB queries
  have no PostgreSQL form because the two schemas are wide and long,
  Graphite has paths and no labels, and Elasticsearch stores per-core
  figures as arrays, which no bucket aggregation can take apart.
- **The PostgreSQL translator**, which rewrites the InfluxDB 3 SQL rather
  than keeping a second copy of every query: `$__dateBin` to `$__timeGroup`,
  `approx_percentile_cont` to `percentile_cont … WITHIN GROUP`, ordered
  aggregates to `(array_agg(… ORDER BY …))[1]`, and a query it cannot
  translate is dropped rather than shipped broken.
- **Seven more tables in the SQL sink**, and two widened: `mikroscope_mem`
  carries the whole of `/proc/meminfo` the agent reads (19 fields, against
  the five it had), and `mikroscope_self` gains the agent's own restart
  count and the kernel-log lines it had to drop. Eleven more `mem.*` fields
  on the Graphite sink, for the same reason: the dashboards ask for them.
- **Two checks ported from the sibling project**, as tests of the container
  suite and against the real stores: every PostgreSQL query `EXPLAIN`ed (209
  of them), and every Prometheus metric name and expression checked against
  a live server (78 names, 182 expressions). Both caught real faults — three
  measurements the SQL sink never wrote an `INSERT` for, and eleven metrics
  that exist only on the agent's own `/metrics`.
- **`dashboards --store`** takes any of the five, and `--var name=value`
  tells `check` what a dashboard's variables stand for, which the browser
  would otherwise resolve and the API path cannot.

### Fixed

- **The four device panels had no data.** The board facts — the identity, the
  thermal trip points, the CPU clock ladder, the cadence each level source is
  read at — were emitted once per capability hash, which in practice is once
  when `forward` starts. They are rows with the collector's clock, so a store
  holds them only at that instant and any dashboard window that does not
  contain it holds nothing: on the reference deployment the last row was 26
  hours old and those four panels had read "No data" for as long. They now
  repeat every five minutes — twelve rows an emission against the 864 000
  sample rows a day that `--hz 10` produces — and the repeat is marked, so the
  stores write it and the streams meant for a reader (Loki, stdout, a
  recording) skip it. Measured on the reference deployment on 2026-09-17:
  emissions five minutes apart to the second, and `dashboards check` over the
  next window returned rows for all four.
- A ceiling or cadence change, which the capability hash does not cover, now
  reaches the sinks at the next repeat instead of waiting for `forward` to
  restart.

### Changed

- **The red dashed lines on every panel have a subsection of their own.** They
  are the detections annotation layer, they are the most conspicuous thing on
  a live dashboard, and the page covered them in three sentences at the end of
  a section about something else. What they are (annotations, not a gap in the
  record and not a slipped tick), what they are for, the two layers and their
  defaults, and where the checkboxes that hide them live — with a capture of
  what a marker looks like.

## [1.0.2]

The release that reaches the boards 1.0.1 could not run on, and a documentation
pass around the question a reader actually arrives with: *what do I download,
and where do I put it?*

### Fixed

- **The hEX Refresh line could not run the agent.** MikroTik's container
  documentation states that the package exists for arm, arm64 and x86 only, and
  that devices with the EN7562CT CPU "support only arm32v5 container images".
  The agent was built `GOARM=7` and its image declared variant `v7`, so every
  board of that family was published for and could not run it: the install
  reports success, the container starts, and it dies with `exec format error`
  in the router's log.

  32-bit ARM is two things now. `--goarm` defaults to **5**, the level that
  starts on every 32-bit ARM MikroTik ships; the image declares the variant it
  was built for; the release publishes `mikroscope-agent-armv5.tar` beside
  `mikroscope-agent-armv7.tar`; and the published image index carries
  `linux/arm/v5` as a fourth platform, so `--remote-image` asks the operator
  nothing. CI builds **and starts** all four platforms under QEMU. What the
  ARMv5 instruction set costs against ARMv7 is not measured: this project has
  no ARM hardware.
- **A panel in the dashboards could not render** where the store has no API
  tier, and **the Site lint CI job never built the site** before running the
  gates that read its output. Both fixed in 1.0.1's window and released here.

### Added

- **[Getting the CLI](https://jmrp.io/docs/mikroscope/install/cli/)**, a page
  in both languages for the question nothing answered: how to have the
  `mikroscope` command at all, on Linux, macOS and Windows, with the archive to
  pick for **your** machine as opposed to the router, the checksum step, the
  macOS quarantine flag, the Windows `PATH`, and `go install`.
- **A troubleshooting page**, by the words you actually see: the RouterOS
  error, the container that exits, the agent that is installed and does not
  answer, the panel that says "No data", the sink that drops.
- **One capture per dashboard section**, twenty-three of them, over a
  demonstration database filled by the same canned fake agent the end-to-end
  suites use. Generated by a script, not taken by hand.
- **A diagram of where the data comes from and where it goes**, generated in
  both languages and at two layouts, inline so it follows the theme.
- Gates that hold the documentation to the code: `stats:check` (the counts the
  pages state are derived from the Go source), `figures:check`, and
  `layout:check`, which drives a browser over every built page at 390 px and
  1280 px and fails on a page that scrolls sideways, a table that does not fit
  its box, or a copy button that covers code.

### Changed

- **The install routes are ordered by what to reach for first**: the registry
  pull is first and recommended — one command, nothing to choose, nothing
  uploaded — and the published tar is last, because it is the only route where
  the operator picks the architecture by hand. The release notes, the README
  and both install pages now carry a table of which file is for which machine.
- **The copy button on every code block is visible**, in a strip of its own
  rather than on top of the code, on every pointer type.
- `playbooks/port-names` is now `reference/port-names`: it is a reference the
  fault case studies link to, not one of them. The old address redirects.

## [1.0.1]

A test release in the literal sense: what changed is what the project can now
say it has tested, and where the image is published.

### The sinks now run against the stores themselves

`test/e2e` points every sink at a capture server and asserts the bytes, which
is the right test for an encoder and cannot prove a store **accepts** them.
`test/e2e/docker` (build tag `dockere2e`, `make test-e2e-docker`) starts nine
stores with docker compose — InfluxDB 3, PostgreSQL, Elasticsearch, Graphite,
Loki, an OpenTelemetry Collector, Telegraf, Prometheus and Grafana — runs the
same collector against the same canned fake agent with every sink pointed at
them, and asks each store its own question with its own API, with the file
sink's JSONL as the oracle, value by value. It then imports both dashboards
into Grafana and runs every panel's query through Grafana's own API.

It needs Docker and no router: the samples are the same canned ones, so a run
is reproducible on any machine. Measured on the development machine on
2026-09-16: the stack comes up in 55–81 s and the suite takes 75–100 s from
nothing.

So the seven sinks that 1.0.0 said were "exercised against fakes, not against
their real servers" now write into the real products and are read back out of
them. What is still true is that none of them has carried samples **from the
router**: file, Prometheus and InfluxDB 3 have, and the other seven have not.

### Fixed

- **The port-event table could not render.** It SELECTs `label` and `role`,
  which the sink writes onto a kernel-log row only from the API tier's
  inventory, and a column that is not in the table is not an empty column on
  InfluxDB 3 — it is `Schema error: No field named label`. The panel now
  declares those fields, so the store probe routes it into the not-available
  row when the collector ran with `--api-mode off`. Found by the new suite on
  its first full run.
- **The Site lint CI job never built the site** before running the gates that
  read `site/dist`, so it failed on the first of them. Nothing caught it
  because that job only runs on a pull request, and this was the repository's
  first one.

### Added

- The agent image is published to **Docker Hub** (`jmrplens/mikroscope-agent`)
  as well as to GHCR. `/container/config registry-url` ships as
  `https://registry-1.docker.io`, so Docker Hub is the registry a RouterOS
  device reaches without being reconfigured first — which is what the
  `--remote-image` and `plan --rsc` routes depend on.
- A reference page, in both languages, on what each of the three test layers
  proves and what none of them do.

### Changed

- `CLAUDE.md` described a project with no Go code in it and pointed at a
  directory that is not in the repository.

## [1.0.0]

The first release. What it contains, what it was measured to cost, and what it
has never been run on.

### The agent

A static Go binary that runs on the router in a `FROM scratch` container and
reads the shared kernel's `/proc`, `/sys`, `/dev/kmsg` and `perf_event_open`
counters on a fixed ticker at 1 to 100 Hz, keeps them in a ring, and serves
them over HTTP on its veth: `/healthz`, `/capabilities`, `/snapshot`,
`/stream`, `/metrics`, `/captures` and `/capture`. It ships raw tick deltas,
never percentages — the averaging window is the reader's choice — and it makes
no outbound connection. Triggered capture pins the full-rate samples around a
condition firing.

### The CLI and collector

`doctor`, `plan`, `install`, `status`, `upgrade` and `uninstall` deploy it over
ssh, listing every write before it happens and verifying every removal by
ownership counts; `record`, `mark` and `plot` capture a window with markers and
draw a deterministic SVG; `forward` merges the kernel tier with the RouterOS
API tier into ten sinks (file, Prometheus, InfluxDB 3, Loki, OTLP, Graphite,
Elasticsearch, SQL, Telegraf, stdout), with a derive stage that adds per-packet
PMU cost, the fast-path share, a memory-pressure state and eleven detection
rules beside the raw rows. `dashboards` generates two Grafana dashboards —
171 panels on InfluxDB, 133 on Prometheus — and eleven alert rules, and checks
them panel by panel against a live Grafana.

### Four ways to install the agent

From a checkout with a Go toolchain; from the published image tar
(`--agent-tar`, no Go needed); by letting the router pull the image itself
(`--remote-image ghcr.io/jmrplens/mikroscope-agent:1.0.0`); or from a RouterOS
script the router runs on its own (`plan --rsc`), with no CLI and no ssh. All
four produce the same container with the same ownership tags.

### Measured on the reference device

An RB5009UG+S+ (arm64, 4× Cortex-A72, 1 GiB) on RouterOS 7.24.2. At the 10 Hz
install default with the full source set: 2.85 % of one core, 31.3 MiB RSS,
0 slipped ticks over a 60 s window; the image is 6.1 MiB against an 8 MiB
budget CI enforces. 10, 50 and 100 Hz were all lossless. A full
`doctor` → `install` → `status` → `upgrade` → `uninstall` round trip left the
router's `/export` byte-identical.

### What this release does not claim

It has run on one device and one RouterOS branch. The arm (32-bit) and x86_64
builds are cross-compiled and CI-checked and have never run on hardware. Seven
of the ten sinks — Loki, OTLP, Graphite, Elasticsearch, SQL, Telegraf and
stdout — are exercised byte for byte by the end-to-end suite against fakes,
not against their real servers. RouterOS 7.24 is the floor: the container step
writes `privileged=`, which earlier releases do not accept.

[1.0.2]: https://github.com/jmrplens/mikroscope/releases/tag/v1.0.2
[1.0.1]: https://github.com/jmrplens/mikroscope/releases/tag/v1.0.1
[1.0.0]: https://github.com/jmrplens/mikroscope/releases/tag/v1.0.0
