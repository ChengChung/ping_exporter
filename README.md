# ping_exporter

Prometheus exporter for ICMP echo requests using https://github.com/digineo/go-ping

This is a simple server that scrapes go-ping stats and exports them via HTTP for
Prometheus consumption. The go-ping library is build and maintained by Digineo GmbH.
For more information check the [source code][go-ping].

[go-ping]: https://github.com/digineo/go-ping

## Getting Started

### Config file

Beaking Change: In v1.2.0 the config file format has changed. If you are using a older config file with custom labels, you will need to update it to the new format (see the example below).

Targets can be specified in a YAML based config file:

```yaml
targets:
  - 8.8.8.8
  - 8.8.4.4
  - 2001:4860:4860::8888
  - 2001:4860:4860::8844
  - host: google.com
    asn: 15169

dns:
  refresh: 2m15s
  nameserver: 1.1.1.1

ping:
  interval: 2s
  timeout: 3s
  history-size: 42
  payload-size: 120
  fw-mark: 222

options:
  disableIPv6: false
```

Note: domains are resolved (regularly) to their corresponding A and AAAA
records (IPv4 and IPv6). By default, `ping_exporter` uses the system
resolver to translate domain names to IP addresses. You can override the
resolver address by specifying the `--dns.nameserver` flag when starting
the binary, e.g.

```console
$ # use Cloudflare's public DNS server
$ ./ping_exporter --dns.nameserver=1.1.1.1:53 [other options]
```

The configuration file is watched via inotify. If the configuration is changed,
ping_exporter will update the targets and labels. To change global options like the ping
interval or history size, you must restart the exporter.

Top-level `labels` apply to every metric, including `ping_up`. Target-specific
labels override global labels on that target's metrics only:

```yaml
labels:
  location: CN
  instance: CAN-CT163
targets:
  - host: 104.245.12.188
    target: xTom.SJC
  - host: 128.1.145.129
    target: Zenlayer.HKG
```

Target labels are flat keys next to `host`, not a nested `labels` mapping.
Configured `target`, `ip`, and `ip_version` labels override the corresponding
built-in values without changing the address being pinged. Global labels and
target labels are reloaded with the configuration. Pushmetrics extra labels,
when configured, take precedence for pushed metrics only.

`--config.path` (or `PING_CONFIG_FILE`, with legacy `CONFIG_FILE` as a fallback)
also accepts an HTTP or HTTPS URL:

```console
$ ./ping_exporter --config.path=https://example.com/ping.yml --config.refresh=30s
```

Remote configurations are fetched at startup and polled every minute by default.
Use `--config.refresh` to set a positive polling interval. Each HTTP request has
a 10-second timeout, and HTTPS uses the system's trusted certificates. The server
must return HTTP 200 with the same YAML format as a local configuration file.
Unchanged configurations are not reapplied. Failed requests, invalid YAML, or
configurations without a targets field are logged and leave the current targets in place;
polling continues so later updates can recover. An initial fetch failure prevents
startup. An explicit `targets: []` is accepted at startup and on reload, and stops
all target monitoring while keeping `ping_up` available. An empty target list is
not replaced by positional command-line targets. As with local files, ping and
DNS options require a restart; labels do not.

### Exported metrics

- `ping_rtt_best_seconds`: Best round trip time in seconds
- `ping_rtt_worst_seconds`: Worst round trip time in seconds
- `ping_rtt_mean_seconds`: Mean round trip time in seconds
- `ping_rtt_std_deviation_seconds`: Standard deviation in seconds
- `ping_loss_ratio`: Packet loss as a value from 0.0 to 1.0

Each metric has labels `ip` (the target's IP address), `ip_version`
(4 or 6, corresponding to the IP version), and `target` (the target's
name).

Additionally, a `ping_up` metric reports whether the exporter
is running (and in which version).

### Shell

To run the exporter:

```console
$ ./ping_exporter [options] target1 target2 ...
```

or

```console
$ ./ping_exporter --config.path my-config-file [options]
```

Help on flags:

```console
$ ./ping_exporter --help
```

Getting the results for testing via cURL:

```console
$ curl http://localhost:9427/metrics
```

### Environment variables and systemd

The flags also accept the `PING_*` environment variables documented in
[ping_exporter.env.example](ping_exporter.env.example). Explicit command-line
values override environment values, which override built-in defaults.
`PING_CONFIG_FILE` takes precedence over the legacy `CONFIG_FILE` variable.
Configuration-file settings retain their existing precedence over flag defaults.

`PING_TARGETS` is a whitespace-separated list used when positional targets are
absent. `PING_PUSHMETRICS_URLS` and `PING_PUSHMETRICS_LABELS` are semicolon-separated
lists; surrounding whitespace is trimmed. Explicit repeated flags replace the
corresponding environment list rather than appending to it. To use a literal
semicolon inside a URL or label, use command-line flags instead.

```console
$ PING_TARGETS="127.0.0.1 ::1" PING_LISTEN_ADDRESS=127.0.0.1:9427 ./ping_exporter
```

For an interactive installation on a Linux host running systemd:

```console
$ go build -o ping_exporter .
$ sudo bash ./install_systemd.sh
```

Rebuild/update the executable first: older binaries do not read these environment
variables. The installer checks for this support and accepts absolute or relative
executable paths, `~/` paths, surrounding quotes, and paths containing spaces.
It requires an existing executable, and either a readable local configuration,
an HTTP(S) configuration URL, or explicit Ping targets. Pushing is optional.
Remote configuration contents are checked by the exporter at startup, not fetched
by the installer.

After confirmation, the installer validates the unit with `systemd-analyze`,
backs up existing files, and writes:

- `/etc/systemd/system/ping_exporter.service` (mode `0644`, runs as root for ICMP).
- `/etc/default/ping_exporter` (mode `0600`, systemd environment-file syntax).

No configuration is written before confirmation. You choose whether to enable
and start/restart the service; declining leaves an existing service running with
its previous configuration until restarted. Existing environment files are
backed up, not merged; reapply any extra settings from the backup.
Edit `/etc/default/ping_exporter` and run `sudo systemctl restart ping_exporter`
to apply subsequent parameter changes. Inspect failures with
`sudo journalctl -u ping_exporter -n 50`.

The equivalent launcher is `sudo bash ./quick_start.sh`; use
`bash ./quick_start.sh --help` for instructions. Isolated installer regression
tests run with `bash ./install_systemd_test.sh` (requires `systemd-analyze`, but
does not require a running systemd daemon or change host services).

### Pushing metrics

Metrics can also be pushed periodically to a VictoriaMetrics Prometheus text
import endpoint while `/metrics` remains available for scraping:

```console
$ ./ping_exporter -pushmetrics.url=http://victoriametrics:8428/api/v1/import/prometheus --pushmetrics.queue-dir=/var/lib/ping_exporter/queue --config.path=my-config-file
```

For a VictoriaMetrics cluster, use
`http://vminsert:8480/insert/0/prometheus/api/v1/import/prometheus` (replace `0`
with the tenant ID). This sends HTTP POST requests with Prometheus text, not
Prometheus remote_write or the Pushgateway protocol. Both `-pushmetrics.*` and
`--pushmetrics.*` flags are accepted.

| Flag | Default | Description |
| ---- | ------- | ----------- |
| `--pushmetrics.url` | empty (disabled) | Full HTTP(S) import URL |
| `--pushmetrics.extraLabel` | none | Repeatable `name="value"` label applied to all pushed metrics |
| `--pushmetrics.interval` | `10s` | Snapshot and retry interval; must be positive |
| `--pushmetrics.timeout` | `10s` | Per-request timeout; must be positive |
| `--pushmetrics.queue-dir` | `pushmetrics-queue` | Writable persistent queue directory |
| `--pushmetrics.max-disk-usage` | `1073741824` | Maximum queued payload bytes (1 GiB) |
| `--pushmetrics.max-inmemory-requests` | `100` | Maximum in-memory batches before spilling; `0` always queues on disk |

Snapshots first enter a FIFO memory queue, retaining their original sample
timestamps in milliseconds. A batch being sent still counts toward the memory
limit until acknowledged. When the queue reaches the configured batch count,
all its batches are written to disk one by one, in order. While any disk batches
remain, new snapshots go directly to disk and the memory queue is not used.
After the disk backlog is completely acknowledged, memory buffering resumes.
Startup with an existing disk backlog also starts in disk-only mode. The memory
limit counts batches, not bytes; actual memory use depends on snapshot sizes.

Network failures and non-2xx responses retain the batch for retry. One sender
replays oldest batches first, independently of periodic collection, and removes
a batch only after a successful response. Restarting with the same directory
recovers disk batches, but unsent batches still in memory are lost when the
process exits. Set `--pushmetrics.max-inmemory-requests=0` to retain the original
write-before-send behavior when restart durability is required for every batch.
HTTPS uses the system trust store; HTTP redirects are not followed.

Use a persistent volume in containers and a separate queue directory for each
process. Keep the directory dedicated to this queue, and drain it before
changing the destination URL. The byte limit covers queued payloads, not
filesystem metadata. If spilling is interrupted by a write error or the disk
limit, already written batches stay on disk and the remaining old batches stay
in memory. New snapshots are rejected with an error log until those batches can
be spilled or drained; batches on disk are always sent first. Disk-only enqueue
failures also lose new snapshots. Delivery of retained batches is at least once:
a lost acknowledgement or a crash after remote acceptance can cause duplicate
samples. Set the remote retention policy to accept the oldest samples you expect
to replay.

When multiple nodes push to the same destination, use extra labels to identify
the node and job:

```console
$ ./ping_exporter -pushmetrics.url=http://victoriametrics:8428/api/v1/import/prometheus \
  -pushmetrics.extraLabel='instance="foobar"' \
  -pushmetrics.extraLabel='job="vm"' \
  --config.path=my-config-file
```

Each flag accepts one `name="value"` pair, with a double-quoted value supporting
escapes such as `\"`, `\\`, and `\n`. Names must match
`[a-zA-Z_][a-zA-Z0-9_]*` and must not start with the reserved `__` prefix.
Extra labels override existing labels with the same name; when a flag repeats
the same name, its last value wins. They apply only to pushed metrics, not
`/metrics`, and are saved in each queued snapshot. Changing flags does not
relabel already queued batches. No `job` or `instance` labels are added unless
configured. Existing YAML target labels are also supported.

The existing `--web.token` only protects incoming web requests and is not sent
to the push endpoint.

### Running as non-root user

On Linux systems `CAP_NET_RAW` is required to run `ping_exporter` as unpriviliged user.

```console
# setcap cap_net_raw+ep /path/to/ping_exporter
```

When run through a rootless Docker implementation on Linux, the flag `--cap-add=CAP_NET_RAW` should be added to the `docker run` invocation.

If being invoked via systemd, you can alternately just add the following
settings to the service's unit file in the `[Service]` section:

```console
CapabilityBoundingSet=CAP_NET_RAW
AmbientCapabilities=CAP_NET_RAW
```

## Docker

https://hub.docker.com/r/czerwonk/ping_exporter

To run the ping_exporter as a Docker container, run:

```console
$ docker run -p 9427:9427 -v /path/to/config/directory:/config:ro --name ping_exporter czerwonk/ping_exporter
```

## Kubernetes

To run the ping_exporter in Kubernetes, you can use the supplied helm chart

### Prerequisites

- Helm v3.0.0+

### Installing the chart

To install the chart with the release name `ping-exporter`:

```console
$ helm repo add ping-exporter "https://czerwonk.github.io/ping_exporter"
"ping-exporter" has been added to your repositories

$ helm repo update
Hang tight while we grab the latest from your chart repositories...
...Successfully got an update from the "ping-exporter" chart repository
Update Complete. ⎈Happy Helming!⎈

$ helm install ping-exporter ping-exporter/ping_exporter
NAME: ping-exporter
...

```

### General parameters

| Key                          | Type   | Default                                                                                            | Description                                                                                             |
| ---------------------------- | ------ | -------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------- |
| affinity                     | object | `{}`                                                                                               | [Affinity]                                                                                              |
| args                         | list   | `[]`                                                                                               | Add additional [command-line arguments] when running ping_exporter                                      |
| config                       | object | see [values.yaml]                                                                                  | Contains the contents of ping_exporter's [config file]                                                  |
| fullnameOverride             | string | `""`                                                                                               | String to fully override `"ping-exporter.fullname"`                                                     |
| image.repository             | string | `"czerwonk/ping_exporter"`                                                                         | String to override the docker image repository                                                          |
| image.pullPolicy             | string | `"IfNotPresent"`                                                                                   | String to override the pullPolicy                                                                       |
| image.tag                    | string | `""`                                                                                               | Overrides the ping_exporter image tag whose default is the chart `appVersion`                           |
| imagePullSecrets             | list   | `[]`                                                                                               | If defined, uses a secret to pull an image from a private Docker registry or repository                 |
| ingress.enabled              | bool   | `false`                                                                                            | Enable an ingress resource for the ping_exporter                                                        |
| ingress.className            | string | `""`                                                                                               | Defines which ingress controller will implement the resource                                            |
| ingress.annotations          | object | `{}`                                                                                               | Annotations to be added to the ingress resource                                                         |
| ingress.hosts                | list   | `[{"host": "chart-example.local", "paths":[{"path": "/", "pathType": "ImplementationSpecific"}]}]` | Defines the [ingress] hosts and path to proxy                                                           |
| ingress.tls                  | list   | `[]`                                                                                               | Defines the secret(s) containing TLS certs for the [ingress] host                                       |
| nameOverride                 | string | `""`                                                                                               | Provide a name in place of `ping-exporter`                                                              |
| podAnnotations               | object | `{}`                                                                                               | Annotations to be added to ping_exporter pods                                                           |
| podSecurityContext           | object | `{}`                                                                                               | Sets the container-level security context                                                               |
| replicaCount                 | number | `1`                                                                                                | Override the number of replicas running                                                                 |
| resources                    | object | `{}`                                                                                               | Defines the ping_exporter pod's resource cpu/memory limits and requests                                 |
| nodeSelector                 | object | `{}`                                                                                               | [Node selector]                                                                                         |
| securityContext.capabilities | object | `{"add": ["CAP_NET_RAW"]}`                                                                         | This object overrided the pod's security context capabilities                                           |
| service.type                 | string | `"ClusterIP"`                                                                                      | Sets the type of kubernetes service which is created for ping_exporter                                  |
| service.port                 | number | `9427`                                                                                             | Sets the port in which the kubernetes service will listen on and communicate with the ping_exporter pod |
| service.annotations          | object | `{}`                                                                                               | Annotations applied to the kubernetes service                                                           |
| serviceAccount.create        | bool   | `true`                                                                                             | Create a service account for the application                                                            |
| serviceAccount.annotations   | object | `{}`                                                                                               | Annotations applied to created service account                                                          |
| serviceAccount.name          | string | `""`                                                                                               | Overrides the application's service account name which defaults to `"ping-exporter.fullname"`           |
| tolerations                  | list   | `[]`                                                                                               | [Tolerations]                                                                                           |
| testConnection.enabled       | bool   | `true`                                                                                             | Enable the test connection pod for the ping_exporter                                                    |

## Changes from previous versions

### `ping_loss_ratio` vs `ping_loss_percent`

Previous versions of the exporter reported packet loss via a metric named
`ping_loss_percent`. This was somewhat misleading / wrong, because it never
actually reported a percent value (it was always a value between 0 and 1). To
make this more clear, and to match with [Prometheus best
practices](https://prometheus.io/docs/practices/naming/#base-units), this
metric has been renamed to `ping_loss_ratio` instead.

If you had already been using an earlier version and want to continue to record
this metric in Prometheus using the old name, this can be done using the
`metric_relabel_configs` options in the Prometheus config, like so:

```console
- job_name: "ping"
  static_configs:
    <...>
  metric_relabel_configs:
    - source_labels: [__name__]
      regex: "ping_loss_ratio"
      target_label: __name__
      replacement: "ping_loss_percent"
```

### Time units

As per the recommendations for [Prometheus best
practices](https://prometheus.io/docs/practices/naming/#base-units), the
exporter reports time values in seconds by default. Previous versions
defaulted to reporting milliseconds by default (with metric names ending in
`_ms` instead of `_seconds`), so if you are upgrading from an older version,
this may require some adjustment.

It is possible to change the ping exporter to report times in milliseconds
instead (this is not recommended, but may be useful for compatibility with
older versions, etc). To do this, the `metrics.rttunit` command-line switch
can be used:

```console
$ # keep using seconds (default)
$ ./ping_exporter --metrics.rttunit=s [other options]
$ # use milliseconds instead
$ ./ping_exporter --metrics.rttunit=ms [other options]
$ # report both millis and seconds
$ ./ping_exporter --metrics.rttunit=both [other options]
```

If you used the `ping_exporter` in the past, and want to migrate, start
using `--metrics.rttunit=both` now. This gives you the opportunity to
update all your alerts, dashboards, and other software depending on ms
values to use proper scale (you "just" need to apply a factor of 1000
on everything). When you're ready, you just need to switch to
`--metrics.rttunit=s` (or just remove the command-line option entirely).

### Deprecated metrics

Previous versions of this exporter provided an older form of the RTT metrics
as:

- `ping_rtt_ms`: Round trip times in millis

This metric had a label `type` with one of the following values:

- `best` denotes best round trip time
- `worst` denotes worst round trip time
- `mean` denotes mean round trip time
- `std_dev` denotes standard deviation

These metrics are no longer exported by default, but can be enabled for
backwards compatibility using the `--metrics.deprecated` command-line flag:

```console
$ # also export deprecated metrics
$ ./ping_exporter --metrics.deprecated=enable [other options]
$ # or omit deprecated metrics (default)
$ ./ping_exporter --metrics.deprecated=disable [other options]
```

## Contribute

Simply fork and create a pull-request. We'll try to respond in a timely fashion.

## License

MIT License, Copyright (c) 2018
Philip Berndroth [pberndro](https://twitter.com/pberndro)
Daniel Czerwonk [dan_nrw](https://twitter.com/dan_nrw)

[Node selector]: https://kubernetes.io/docs/user-guide/node-selection/
[Ingress]: https://kubernetes.io/docs/concepts/services-networking/ingress/
[Tolerations]: https://kubernetes.io/docs/concepts/configuration/taint-and-toleration/
[Affinity]: https://kubernetes.io/docs/concepts/configuration/assign-pod-node/
[command-line arguments]: https://github.com/czerwonk/ping_exporter#different-time-unit
[config file]: https://github.com/czerwonk/ping_exporter#config-file
[values.yaml]: https://github.com/czerwonk/ping_exporter/blob/main/dist/charts/ping-exporter/values.yaml
