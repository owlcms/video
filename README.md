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

Detailed instructions: https://owlcms.github.io/owlcms4-prerelease/#/JuryReplays

## Configuration

All three configuration files live in a single directory, selected with `--configDir` (default `./video_config`):

| File | Owner | Contents |
| --- | --- | --- |
| `cameras.toml` | Cameras | the camera inventory and the streams it publishes |
| `replays.toml` | Replays | which cameras are used, in what order, and the owlcms connection |
| `ffmpeg.toml` | Cameras | encoder and capture settings |

Run with `--extractConfig` to create the files with their default contents.

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
