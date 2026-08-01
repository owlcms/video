# Video Streaming Configuration

The **cameras** program captures USB cameras, encodes the video to H.264, and
streams it as MPEG-TS over UDP to one or more receivers (replays, OBS, etc.).

Two streaming modes are available:

| Mode | How it works | Best for |
|------|-------------|----------|
| **Multicast** (default) | Single UDP stream to a multicast group; receivers join the group | Managed switches with IGMP snooping |
| **Unicast tee** | ffmpeg `tee` sends one independent UDP copy per destination | Dumb / unmanaged switches, consumer routers |

On a **dumb switch**, multicast frames are flooded to every port (including
uplinconfiks) because there is no IGMP snooping.  Unicast frames are forwarded only
to the port whose MAC address the switch has learned.

---

## Option A — Multicast (default)

### Network topology

```
[Camera sender]   ──┐
[OBS-A]           ──├── managed switch (IGMP snooping) ──── uplink
[OBS-B]           ──┘
```

The switch must support **IGMP snooping** to prevent flooding.

### Camera sender — `config.toml`

```toml
[multicast]
    ip = "239.255.0.1"
    startPort = 9001
    pktSize = 1316
    localOnly = false

[unicast]
    enabled = false
```

Each camera is assigned a sequential port (camera 1 = 9001, camera 2 = 9002, …).
The single multicast group carries all traffic; receivers join the group to
receive it.

### Replays receiver — `config.toml` `[mpeg-ts]`

```toml
[mpeg-ts]
    enabled = true
    ip = "239.255.0.1"
    camera1Port = 9001
    camera2Port = 9002
    camera3Port = 0
    camera4Port = 0
```

ffmpeg joins the multicast group via IGMP.

### OBS receivers — Media Source

| Setting              | Value                                |
|----------------------|--------------------------------------|
| Local File           | **unchecked**                        |
| Input                | `udp://239.255.0.1:9001`            |
| Input Format         | `mpegts`                             |
| Reconnect Delay      | `2`s                                 |
| Network Buffering    | `100`–`300` ms                       |

For camera 2, use port `9002`.

---

## Option B — Unicast tee

### Network topology

```
[Camera sender]   ──┐
[OBS-A]           ──├── dumb switch ──── uplink ──── router / main LAN
[OBS-B]           ──┘
```

All machines must be on the **same subnet** (e.g. `192.168.1.0/24`).
If a destination IP is on a different subnet the packets are routed through the
default gateway (uplink), defeating the purpose — see the double-NAT option below
if you need full isolation.

### Bandwidth

With 2 cameras at 8 Mb/s and 2 remote destinations:

    2 cameras × 2 remote copies × 8 Mb/s = 32 Mb/s

The local `127.0.0.1` destination does not use the NIC.  A USB-3 gigabit adapter
handles ~940 Mb/s — 32 Mb/s is about 3.4% of its capacity.

### Camera sender — `config.toml`

```toml
[unicast]
    enabled = true

    # Starting port (camera 1 = 9001, camera 2 = 9002, …)
    startPort = 9001

    # UDP packet size for MPEG-TS
    pktSize = 1316

    # Every machine that needs to receive the stream.
    # 127.0.0.1 = local replays/ffmpeg on the sender machine.
    # Add the IP of each remote OBS or replays instance.
    destinations = [
        "127.0.0.1",
        "192.168.1.21",
        "192.168.1.22",
    ]
```

When `unicast.enabled = true`, the `[multicast]` section is ignored.

The cameras program will print at startup:

```
Starting camera streams (unicast tee):
=======================================

[mjpeg] Logitech C920 (1920x1080, mjpeg @ 30 fps)
  -> udp://127.0.0.1:9001
  -> udp://192.168.1.21:9001
  -> udp://192.168.1.22:9001
```

### Replays receiver — `config.toml` `[mpeg-ts]`

Set `ip` to `0.0.0.0` so ffmpeg opens a passive UDP listener (no IGMP join).
This works whether replays is on the sender machine or on a remote host:

```toml
[mpeg-ts]
    enabled = true
    ip = "0.0.0.0"
    camera1Port = 9001
    camera2Port = 9002
    camera3Port = 0
    camera4Port = 0
```

This produces ffmpeg input URLs like `udp://0.0.0.0:9001` (listen on all
interfaces).

> **Note:** Do not use a multicast group address (e.g. `239.255.0.1`) in unicast
> mode — ffmpeg would issue an IGMP join that never receives traffic.

### OBS receivers — Media Source

On each OBS machine, add a **Media Source** per camera:

| Setting              | Value                  |
|----------------------|------------------------|
| Local File           | **unchecked**          |
| Input                | `udp://@:9001`         |
| Input Format         | `mpegts`               |
| Reconnect Delay      | `2`s                   |
| Network Buffering    | `100`–`300` ms (adjust if unstable) |
| Restart when active  | **checked**            |

For camera 2, use port `9002`.

The `@` in the URL means "bind all local interfaces" (equivalent to `0.0.0.0`).
You can also use the machine's own IP explicitly, e.g. `udp://192.168.1.21:9001`.

---

## Option C — Double-NAT isolation (multicast or unicast)

If video traffic must be **completely isolated** from the primary LAN — even
preventing stray broadcasts or misconfigured destinations from leaking — place a
second router between the video switch and the primary network.

### Requirements

The video-side router must use a **different IP subnet** than the primary LAN.
This is a standard double-NAT topology: the video router's WAN port gets a
primary-LAN address, and its LAN side runs a separate DHCP range for the video
devices.

### Network topology

```
                          video subnet                        primary subnet
                          192.168.2.0/24                      192.168.1.0/24

[Camera sender .10]  ──┐                    ┌── WAN .50 ──┐
[OBS-A         .21]  ──├── dumb switch ─────┤ video router ├──── primary router ── internet
[OBS-B         .22]  ──┘                    └── LAN .1  ──┘     192.168.1.1
```

- **Video router WAN:** `192.168.1.50` (address on primary LAN)
- **Video router LAN/DHCP:** `192.168.2.1`, range `192.168.2.10–192.168.2.99`
- All video devices get `192.168.2.x` addresses

### Why it works

| Traffic type | Behavior |
|-------------|----------|
| Unicast between `192.168.2.x` devices | Stays on the dumb switch — never reaches the video router |
| Multicast from `192.168.2.x` | Flooded on the dumb switch but **stopped at the video router** (consumer routers do not forward multicast across NAT) |
| Broadcast (ARP, DHCP) | Limited to the `192.168.2.0/24` broadcast domain |
| Video devices accessing the internet | NAT'd through the video router, then through the primary router (double-NAT) |

### Trade-offs

| Pro | Con |
|-----|-----|
| Complete traffic isolation — nothing leaks to primary LAN | Video devices are double-NAT'd (no inbound connections from primary LAN) |
| Works with both multicast and unicast modes | Extra router hardware (any cheap consumer router works) |
| No managed switch or VLAN knowledge required | owlcms on the primary LAN cannot directly reach video devices (use port forwarding on the video router if needed) |
| Video devices can still reach the internet and owlcms | Slightly more complex network setup |

### Configuration

Use either multicast or unicast mode as documented above — the only difference is
that all IP addresses are on the `192.168.2.0/24` video subnet instead of
`192.168.1.0/24`.

If replays or owlcms needs to reach devices across the NAT boundary, set up port
forwarding on the video router for the required ports (e.g. 8080 for owlcms,
9001–9004 for camera streams).

---

## Switching between modes

| From → To | Changes needed |
|-----------|---------------|
| Multicast → Unicast | cameras `config.toml`: set `unicast.enabled = true` + fill `destinations`; replays `config.toml` `[mpeg-ts]`: change `ip` to `0.0.0.0` |
| Unicast → Multicast | cameras `config.toml`: set `unicast.enabled = false`; replays `config.toml` `[mpeg-ts]`: change `ip` to `239.255.0.1` |
| Flat LAN → Double-NAT | Add video router, re-address video devices to new subnet, update all config IPs |

---

## Troubleshooting

| Symptom | Cause | Fix |
|---------|-------|-----|
| OBS shows black / no signal | Destination IP wrong or firewall blocking UDP | Verify IP in `destinations`, open port in firewall |
| Intermittent glitches/freezes | USB-3 adapter driver issue or jitter | Try a different adapter (ASIX/Intel chipset), increase OBS network buffer |
| Video leaks to main LAN (unicast) | Destination IP is on a different subnet | Ensure all machines are on the same `/24` |
| Video leaks to main LAN (multicast) | Dumb switch floods multicast | Use unicast mode, or add a video router (double-NAT) |
| replays gets no video | `config.toml` `[mpeg-ts]` still has `239.x.x.x` in unicast mode | Change `ip` to `0.0.0.0` |
| High CPU on sender | Software encoding 2× 1080p streams | Check that hardware encoder (`h264_qsv`, `h264_nvenc`) is detected |
| Video devices can't reach owlcms (double-NAT) | NAT blocks inbound from primary LAN | Port-forward owlcms port on the video router |
