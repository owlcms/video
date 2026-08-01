# Developer Profile (Build + Debug Tooling)

This profile is for developers fixing build/runtime issues.

## 1) Pull latest scripts/docs

```bash
type -p curl >/dev/null || sudo apt install -y curl
GH_KEY_URL="https://cli.github.com/packages/githubcli-archive-keyring.gpg"
GH_KEYRING="/usr/share/keyrings/githubcli-archive-keyring.gpg"
curl -fsSL "$GH_KEY_URL" | sudo tee "$GH_KEYRING" > /dev/null
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

## 2) Run the developer setup script

```bash
cd ~/git/video
chmod +x ./scripts/setup-developer-tools.sh
./scripts/setup-developer-tools.sh
```

The script installs:

- GCC/build essentials
- OpenGL/X11 dev packages for Fyne/GLFW
- Go from tarball (pinned by version in script)
- VS Code
- GitHub CLI (`gh`)
- `CGO_ENABLED=1`

## 3) Verify build prerequisites

```bash
gcc --version
go version
go env CGO_ENABLED
```

For this project:

```bash
cd ~/git/video
go build -o video .
```
