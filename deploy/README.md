# Ready-made stacks

Two compose files, each a collector and somewhere for it to write, with the
dashboard already in the Grafana beside it.

| File | What it starts | Grafana |
| --- | --- | --- |
| [`compose.influxdb-grafana.yaml`](compose.influxdb-grafana.yaml) | InfluxDB 3, Grafana, the collector | datasource and dashboard published by the collector on start |
| [`compose.prometheus-grafana.yaml`](compose.prometheus-grafana.yaml) | Prometheus scraping the collector, Grafana | import the dashboard yourself; see below |

Both need an agent already installed on a router —
[Install the agent](https://jmrp.io/docs/mikroscope/install/) — and a `.env`
beside the compose file naming the `/30` that install was given:

```sh
MIKROSCOPE_SUBNET=172.30.10.0/30
MIKROSCOPE_TOKEN=            # only if the install has one
GRAFANA_PASSWORD=admin
```

Then `docker compose -f compose.influxdb-grafana.yaml up -d`.

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

The collector publishes a datasource for the stores it WRITES to, because a
write URL is an address Grafana can query. Prometheus is scraped instead, so
the collector does not know the address Grafana should use and will not invent
one. Make the datasource, then:

```sh
mikroscope dashboards import --store prometheus \
  --grafana http://localhost:3000 --datasource-uid <uid>
```

## What these are not

A production deployment. Both bind their ports to `127.0.0.1`, InfluxDB starts
`--without-auth` because a store nobody has written to yet has nobody to have
made a credential, and Grafana's password is whatever `.env` says. Put the
stack behind something before anything but localhost can reach it.
