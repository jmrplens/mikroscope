# Ready-made stacks

Two compose files, each a collector and somewhere for it to write, plus a
Grafana beside it.

| File | What it starts | Grafana |
| --- | --- | --- |
| [`compose.influxdb-grafana.yaml`](compose.influxdb-grafana.yaml) | InfluxDB 3, Grafana, the collector | datasource and dashboard published by the collector on start, once `GRAFANA_TOKEN` is in `.env` |
| [`compose.prometheus-grafana.yaml`](compose.prometheus-grafana.yaml) | Prometheus scraping the collector, Grafana | not published; see [below](#prometheus-and-the-dashboard) |

The collector image in both is `ghcr.io/jmrplens/mikroscope:latest`. Pin it to a
release tag (`:1.2.0`, say) for a deployment you want to be able to reproduce,
as for the agent.

Both need an agent already installed on a router —
[Install the agent](https://jmrp.io/docs/mikroscope/install/) — and a `.env`
beside the compose file naming the `/30` that install was given:

```sh
MIKROSCOPE_SUBNET=172.30.10.0/30
MIKROSCOPE_TOKEN=            # only if the install has one
GRAFANA_PASSWORD=admin
GRAFANA_TOKEN=               # InfluxDB stack: a Grafana service-account token, step 2 below
```

A fresh Grafana has no service-account token until someone makes one, so the
InfluxDB stack comes up in three steps:

1. `docker compose -f compose.influxdb-grafana.yaml up -d grafana`
2. In Grafana (`http://localhost:3000`, `admin` and `GRAFANA_PASSWORD`), create
   a service account with the Admin role and a token for it, and put the token
   in `.env` as `GRAFANA_TOKEN=glsa_…`.
3. `docker compose -f compose.influxdb-grafana.yaml up -d`

Without `GRAFANA_TOKEN` the collector still collects into InfluxDB, but logs
`grafana: could not publish, carrying on without it: --grafana needs
GRAFANA_TOKEN…` and Grafana stays empty.

## Why the collector is on the host's network

The agent answers on a `/30` veth **inside the router**, and the route to it
belongs to the host: a container on a bridge network has no way there. So the
collector runs with `network_mode: host`, which is the same networking the
binary has when you run it directly, and it reaches InfluxDB and Grafana at
`127.0.0.1` rather than by service name.

The check before you start: if `curl http://172.30.10.2:9123/healthz` answers
on the host, the collector will work. If it does not, the host has no route to
the veth and neither will the collector.

## If the host has no route to the veth

The relay is the other way in. `--transport relay` pulls the ring through the
RouterOS API with `/tool fetch`, so the collector needs to reach the router's
API port and nothing else — which ordinary networking gives it, bridge network
included. Add to the collector service:

```yaml
    environment:
      MIKROSCOPE_API_ADDR: 192.168.88.1:8728
      MIKROSCOPE_API_USER: mikroscope
      MIKROSCOPE_API_PASSWORD: …
    command: [forward, --transport=relay, --influx-db=mikroscope]
```

It costs round trips: `/tool fetch output=user` returns at most 64 512 bytes,
so a pull is capped at 13 samples where the direct transport takes hundreds.
At 10 Hz that is a fetch every 1.3 s, and a fetch is the expensive part — see
[the relay](https://jmrp.io/docs/mikroscope/install/reaching-the-agent/).

There is **no third way at present**: the collector derives the agent's
address from `--subnet` and cannot be pointed at an arbitrary one, so an
`install --expose` published on the router's LAN address is reachable by curl
but not by `forward`.

## Prometheus and the dashboard

The collector describes a datasource for the stores it WRITES to, because a
write URL is an address Grafana can query. Prometheus is scraped instead, so
the collector does not know the address Grafana should use and will not invent
one, and the compose file does not pass `--grafana`. Two ways to get the
dashboard, both needing a `GRAFANA_TOKEN` made as in step 2 above:

- Create the datasource yourself (URL `http://prometheus:9090`, which is how the
  Grafana container reaches Prometheus), then import the dashboard bound to it:

  ```sh
  GRAFANA_TOKEN=glsa_… mikroscope dashboards import --store prometheus \
    --grafana http://localhost:3000 --datasource-uid <uid>
  ```

- Or tell the collector the address and let it publish both on start: add
  `--grafana=http://127.0.0.1:3000` and
  `--grafana-datasource-url=http://prometheus:9090` to its `command`, and
  `GRAFANA_TOKEN: ${GRAFANA_TOKEN:-}` to its `environment`.

## What these are not

A production deployment. Grafana, InfluxDB and Prometheus bind their ports to
`127.0.0.1`, but the Prometheus stack's collector runs on the host's network and
serves `--prom=0.0.0.0:9124` on **every** host interface, LAN included, with no
token, because Prometheus reaches it through `host.docker.internal`. Firewall
9124 on the host, or narrow the address to the docker bridge's. InfluxDB starts
`--without-auth` because a store nobody has written to yet has nobody to have
made a credential, and Grafana's password is whatever `.env` says. Put the
stack behind something before anything but localhost can reach it.
