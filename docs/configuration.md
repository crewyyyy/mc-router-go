# Configuration

The router accepts exactly one TOML document up to 1 MiB. Unknown keys and wrong value types are errors. `--check-config` validates the file without opening sockets or applying network changes.

## Example

```toml
[server]
listen = "0.0.0.0:25565"

[routes]
"horizon.example.com" = "127.0.0.1:25567"

[default]
backend = "127.0.0.1:25568"

[limits]
max_connections = 256
max_pending_handshakes = 64
max_connections_per_ip = 64
max_ip_entries = 4096
connection_rate = 50.0
connection_burst = 200
ip_connection_rate = 10.0
ip_connection_burst = 40
ip_entry_ttl = "10m"
handshake_timeout = "5s"
handshake_max_bytes = 2048
dial_timeout = "3s"
write_timeout = "15s"
idle_timeout = "30m"
half_close_timeout = "5s"
shutdown_timeout = "10s"
stats_interval = "30s"

[transparent]
enabled = false

[logging]
level = "info"
```

The source of truth for a complete default config is [`../configs/config.example.toml`](../configs/config.example.toml).

## Keys and defaults

| TOML key | Default | Validation / meaning |
|---|---:|---|
| `server.listen` | `127.0.0.1:25565` | Literal IP and nonzero port. Transparent mode requires IPv4. Use `0.0.0.0:25565` only when the host firewall permits it. |
| `[routes]` | empty | Map of normalized Minecraft hostname to `host:port`. Keys are exact routes after DNS/IDNA normalization. |
| `default.backend` | empty | Optional backend for unknown hosts. At least one route or a default is required. |
| `transparent.enabled` | `false` | Linux, IPv4, non-loopback literal IPv4 backend and externally prepared namespace/policy route. Experimental. |
| `limits.max_connections` | `256` | Maximum active TCP handlers, including incomplete handshakes. `1..1,000,000`. |
| `limits.max_pending_handshakes` | `64` | Separate cap on sessions that have not completed the bounded handshake. `1..max_connections`. |
| `limits.max_connections_per_ip` | `64` | Active plus pending sessions for one observed source IP. Must not exceed total cap. Many players behind a shared NAT count as one IP. |
| `limits.max_ip_entries` | `4096` | Hard cap on attacker-controlled per-IP rate state. |
| `limits.connection_rate` / `ip_connection_rate` | `50.0` / `10.0` | Token refill per second. These defaults are conservative starting values, not a tested capacity promise. |
| `limits.connection_burst` / `ip_connection_burst` | `200` / `40` | Maximum token burst. |
| `limits.ip_entry_ttl` | `10m` | Expiration age for inactive per-IP rate records. |
| `limits.handshake_timeout` | `5s` | Absolute wall-clock deadline from TCP accept, not a timeout reset on each byte. |
| `limits.handshake_max_bytes` | `2048` | Maximum first frame allocation. Must be `128..4096`. |
| `limits.dial_timeout` | `3s` | Maximum upstream TCP connect time. |
| `limits.write_timeout` | `15s` | Maximum duration of each bounded output chunk. |
| `limits.idle_timeout` | `30m` | Session inactivity in both directions. Any direction's traffic resets the shared timer. |
| `limits.half_close_timeout` | `5s` | Maximum reverse drain after one direction reaches EOF. |
| `limits.shutdown_timeout` | `10s` | Stop-accept/drain window before active sockets are cancelled. |
| `limits.stats_interval` | `30s` | Aggregate log interval. No IP/high-cardinality metric labels are emitted. |
| `logging.level` | `info` | `debug`, `info`, `warn`, `error`. Protocol-controlled strings are normalized and JSON encoded. |

Duration values must be TOML strings such as `"5s"`, `"500ms"`, `"30m"`. Integer durations, zero, negative and values over 24 hours are rejected.

An omitted key inherits the documented default. If an operator lowers only `max_connections`, omitted `max_pending_handshakes` and `max_connections_per_ip` are clamped to the new total. Explicit zero is not treated as “unset”; it is rejected.

## Upstream addresses

In standard mode, a backend is a literal `IP:port` or an administrator-selected DNS `hostname:port`. This enables service DNS on a user-defined Docker network. The client-provided Minecraft hostname **never** becomes an upstream address; it only selects among the finite `[routes]` map and the configured default.

IPv6 standard backends use brackets, for example `[2001:db8::10]:25565`. In transparent mode every backend must instead be a non-loopback literal IPv4 endpoint. A DNS name or `127.0.0.1` fails validation.

A checked config proves only syntactic/configuration validity. It does not prove a backend is reachable, that firewall rules are installed, or that transparent return routing works.
