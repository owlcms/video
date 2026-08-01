**Change log**

- 1.0.0: First release of OWLCMS Video, the successor to the separate Cameras and Replays programs.
  - Cameras and Replays are now a single program with one installation and one configuration directory.
  - The **Modules** menu selects which tabs are shown; `--cameras`, `--replays`, `--no-cameras` and `--no-replays` do the same from the command line.
  - Configuration is a single directory holding `cameras.toml`, `replays.toml` and `ffmpeg.toml`, selected with `--configDir` and created with `--extractConfig`.
  - Replays reads the camera inventory from the shared `cameras.toml` when both modules run on the same machine, and over HTTP when Cameras runs elsewhere.
  - The web endpoints are unchanged, so existing OBS browser sources keep working.

Earlier history is in the [owlcms/replays](https://github.com/owlcms/replays) repository, which is no longer maintained.
