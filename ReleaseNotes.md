**Change log**

- 3.1.0-rc01: Replays access tokens, latest-clip endpoint, unicast fixes
  - Replays can be protected with named, individually revocable 21-day viewer
    tokens managed from **Replays > Access Tokens...**. Browser sign-in,
    token-bearing replay URLs and HTTPS reverse proxies are supported. With no
    tokens configured, Replays remains open as before.
  - `/replay/N` returns the latest finished clip for camera N, read from the
    replays folder as on the replays page, including after a restart. It no
    longer waits up to 45 seconds for a clip being recorded or trimmed, and no
    longer cuts off a download when the next recording starts.
  - Stopping Video with Ctrl-C or SIGTERM no longer crashes with "The GLFW
    library is not initialized".
  - On Windows, the local `127.0.0.1` unicast destination was wrongly reported as unreachable and unchecked at startup. The local destination is no longer checked and always stays enabled, since it feeds local preview and Replays.
  - On Windows, a remote unicast destination whose machine is up is no longer disabled just because nothing listens on the TCP port used for the reachability check.
  - **Replays > Use Streams from Cameras Server** now detects when the Cameras machine is not sending to the Replays machine. The Cameras machine's own `127.0.0.1` destination was mistaken for the Replays machine, so a missing destination was never reported. The message names the address to add on the Cameras machine, or says that it is present but unchecked. **Select Cameras** applies the same check when it fetches from the saved Cameras machine.
  - The Cameras machine address defaults to port 8091 when none is given. The example address previously suggested port 8090, which is not used by Video.

- 3.0.2: Camera detection and Replays camera selection UI improvements
  - On macOS, built-in cameras are now correctly hidden unless **Cameras > Include Internal Cameras** (`includeAll`) is on.
  - **Replays > Select Cameras** offers every enabled camera.
  - The Configuration tab shows the detected sources as soon as the scan finishes
  - Detected Sources lists the Name column first, and shows "No Sources Detected" when there are none.
  - The Configuration tab layout is updated when a rescan adds or removes sources. The RTSP Sources section could previously overlap the detected sources.
  - Fixed "Error in Fyne call thread" warnings when an unreachable RTSP camera is retried in the background, and when the program is stopped with Ctrl-C or from the Control Panel. Camera streams and the Replays services are now always shut down in that case.
  - An enabled RTSP camera that cannot be reached is retried until it comes back with an exponential backoff to 16 seconds between retries. It previously gave up after 3 retries. USB cameras are not retried.
  - The Monitoring tab shows camera names without the `[uid:...]` suffix.
  - The startup check of the camera streams is now called the "replay sampling test" in messages and logs.
  - New `--simulate` option for demonstrations without working cameras: built-in cameras are hidden, no streams are started, and each lift creates empty replay files that appear in the replay list like real ones.

- 3.0.1: macOS changes for camera permissions when running without a terminal

- 3.0.0: First release of OWLCMS Video, the successor to the separate Cameras and Replays programs.
  - Cameras and Replays are now a single program with one installation and one configuration directory.
  - The **Modules** menu selects which tabs are shown; `--cameras`, `--replays`, `--no-cameras` and `--no-replays` do the same from the command line.
  - Configuration is a single directory holding `cameras.toml`, `replays.toml` and `ffmpeg.toml`, selected with `--configDir` and created with `--extractConfig`.
  - Replays reads the camera inventory from the shared `cameras.toml` when both modules run on the same machine, and fetches it over HTTP from Cameras' `/api/cameras/config` endpoint when Cameras runs on a separate machine.
  - The web endpoints are unchanged, so existing OBS browser sources keep working.
  - On macOS checks for a homebrew-installed version of ffmpeg before the system path.

Earlier history is in the [owlcms/replays](https://github.com/owlcms/replays) repository, which is no longer maintained.
