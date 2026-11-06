# OWLCMS Video

OWLCMS Video is a single application that captures the competition cameras and produces automatic jury replays for owlcms.

It replaces the two separate programs that were previously published from the [owlcms/replays](https://github.com/owlcms/replays) repository. That repository is no longer maintained.

## Modules

The application hosts two modules, shown as tabs. Either can be hidden from the **Modules** menu or from the command line.

- **Cameras** — discovers the connected capture devices, encodes each one, and publishes the streams. The Monitoring tab shows the live state of every stream; the Configuration tab manages the device and encoder settings.
- **Replays** — records the streams published by the Cameras module and, using the clock and decision information sent by owlcms, produces trimmed replay videos that the jury watches in a web browser.

Running both modules in one process is the normal setup: Replays reads the camera inventory directly from the shared configuration. The modules can also run on separate machines, in which case Replays fetches the camera list over HTTP from the machine running Cameras.  You can select which modules run from the `Modules` menu if you need a split setup.

As an additional benefit, this creates a full video archive of all the lifts in the competition, organized by session and labelled with the athlete, time of day, lift type and attempt number.

## Supported platforms

- Windows 10/11 on a recent laptop (a good GPU is required for multiple cameras)
- macOS (Apple Silicon and Intel)
- Linux, including Raspberry Pi 5 / Raspberry Pi 500 with an SSD or a large-enough SD card

## Installation and use

Install and update OWLCMS Video from the OWLCMS Control Panel.

Detailed instructions: https://jflamy.github.io/owlcms4/#/060-Refereeing/030-JuryReplays

## Configuration

All three configuration files live in a single directory, selected with `--configDir` (default `./video_config`):

| File | Owner | Contents |
| --- | --- | --- |
| `cameras.toml` | Cameras | the camera inventory and the streams it publishes |
| `replays.toml` | Replays | which cameras are used, in what order, and the owlcms connection |
| `ffmpeg.toml` | Cameras | encoder and capture settings |

Run with `--extractConfig` to create the files with their default contents.

### Replays access tokens

Replays are open to anyone who can reach the server until a token is added.
In the Video application's **Replays > Access Tokens...** menu, add a named
token for each broadcaster and separate tokens for OBS and Tracker. All tokens
grant the same replay access. A token is shown only when created or regenerated
and expires after 21 days; distribute it securely.

Revoking or regenerating one token invalidates that holder's future requests
and browser sessions without affecting the other tokens. A download or
WebSocket already open is not interrupted. Revoking the last token opens
Replays to everyone again; the confirmation warns about this. Expired tokens
remain in the list and do not reopen access. **Open access** deletes all
tokens and makes Replays accessible without credentials.

Broadcasters may open the Replays page and enter their token in the sign-in
form, or follow `https://a.replays.example.org/?token=TOKEN`. The link signs
the browser in and removes the token from the address bar. Players and scripts
may use `?token=TOKEN` on each replay URL or an `Authorization: Bearer TOKEN`
header. Treat token-bearing links like passwords: they may be stored in
browser history, player settings or other logs.
See the [broadcaster guide](docs/replay-broadcaster.md) for a handout.

Video stores token hashes and its session signing key in `video-access.json`.
With the Control Panel, this is in the `owlcms-video` installation directory,
above the version folders. A standalone launch stores it in `--configDir`;
`VIDEO_SECURITYDIR` overrides the location. The file is owner-only on Unix.
On Windows, protect the installation directory with an appropriate ACL; a
standalone directory shared with other users does not protect the token file.

For external access, use HTTPS through a trusted reverse proxy rather than
forwarding Video's port directly. With one Video laptop per platform:

1. Give each platform a DNS name pointing to the router's external address
   (a CNAME to its dynamic DNS name works). Forward ports 80 and 443 to Caddy,
   not Video's port 8091.
2. In each laptop's `replays.toml`, set `trustedProxies` to Caddy's LAN address
   (or `127.0.0.1` if Caddy is local) and `publicUrl` to that platform's public
   HTTPS origin. Only trust a proxy under your control; do not trust the router
   or the entire LAN.
3. Configure Caddy with one site per platform:

   ```caddyfile
   a.replays.example.org {
       respond /api/cameras/config 404
       reverse_proxy 192.168.1.50:8091
   }
   b.replays.example.org {
       respond /api/cameras/config 404
       reverse_proxy 192.168.1.51:8091
   }
   ```

Caddy obtains and renews certificates and proxies WebSockets. The connection
from Caddy to Video is plain HTTP, so the LAN and connected hosts must be
trusted. The Cameras configuration endpoint remains unprotected on that LAN
and may contain camera credentials; Caddy blocks it externally.

Do not enable request access logs containing token-bearing URLs or credentials.
Caddy does not enable access logs by default. If access logging is enabled,
filter `request>uri`, `request>headers>Referer`, `request>headers>Cookie` and
`request>headers>Authorization` out of the logs, and verify the resulting log
with a test token before distributing real tokens.

## Command line

```
--configDir <dir>   directory holding cameras.toml, replays.toml and ffmpeg.toml
--extractConfig     create the missing configuration files and exit
--cameras           force the Cameras module visible for this launch
--replays           force the Replays module visible for this launch
--no-cameras        force the Cameras module hidden for this launch
--no-replays        force the Replays module hidden for this launch
--all               include every camera source, including raw formats
--startport <n>     first port used for multicast allocation
--simulate          simulate captures: Cameras hides built-in cameras and does not start streams; Replays writes empty replay files instead of recording
```

These switches temporarily override the module selection saved by the **Modules** menu; they do not change it. Changes made from the **Modules** menu are saved for future launches.

## Equipment setup

The application is designed to run on the jury laptop. Typical setups:

- A dedicated good-quality USB webcam connected with an active USB cable, to account for the distance.
- A regular camera with an HDMI output connected through an HDMI-to-USB capture adapter. If the camera also feeds the stream, a splitter or a pass-through adapter is used.
- Professional SDI feeds from the cameras. The SDI-to-USB conversion takes place in the video control room and the jury reaches the replays with a browser. Several instances can run on a single computer if needed.

## Building

```bash
go build -o video .
```

See [DEVNOTES.md](DEVNOTES.md) for the development workflow.
