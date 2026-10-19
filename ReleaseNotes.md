**Change log**

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
