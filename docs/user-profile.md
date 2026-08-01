# User Media Profile (OBS PipeWire)

This profile is for non-developers who just need media playback/recording working.

## 1) Pull latest scripts/docs

(This will likely be copied from a browser opening the docs on github.com)

```bash
GH_KEY_URL="https://cli.github.com/packages/githubcli-archive-keyring.gpg"
GH_KEYRING="/usr/share/keyrings/githubcli-archive-keyring.gpg"
wget -qO- "$GH_KEY_URL" | sudo tee "$GH_KEYRING" > /dev/null
sudo chmod go+r "$GH_KEYRING"
ARCH="$(dpkg --print-architecture)"
cat <<EOF | sudo tee /etc/apt/sources.list.d/github-cli.list > /dev/null
deb [arch=${ARCH} signed-by=${GH_KEYRING}] https://cli.github.com/packages stable main
EOF
sudo apt update
sudo apt install -y gh

mkdir -p ~/git && cd ~/git
gh repo clone owlcms/video || true
cd ~/git/video
```

## 2) Run the OBS setup script

```bash
cd ~/git/video
chmod +x ./scripts/setup-user-obs.sh
./scripts/setup-user-obs.sh
```

The script installs:

- OBS Studio
- OBS PipeWire plugin from GitHub releases (`dimtpap/obs-pipewire-audio-capture`)

## 3) Install FFmpeg/FFplay on PATH (Jellyfin copy)

Control Panel bundles its own `ffmpeg`/`ffplay`, but those binaries may depend on bundled runtime library paths.  We install a ffmpeg and ffplay for CLI convenience.


```bash
cd ~/git/video
chmod +x ./scripts/setup-user-ffplay.sh
./scripts/setup-user-ffplay.sh
```

## 4) Verify

```bash
ffmpeg -version
ffplay -version
ffmpeg -hide_banner -encoders | grep nvenc
pactl info | grep "Server Name"
```

`pactl` should show `PulseAudio (on PipeWire)`.

## 5) In OBS

Add source: **Application Audio Capture (PipeWire)**.
