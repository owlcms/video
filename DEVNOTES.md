### Project Structure

- `main.go`: Application entry point (hosts the Cameras and Replays modules)
- `internal/`: Private application code
  - `cameras/`: Cameras module UI and stream management
  - `replays/`: Replays module UI, HTTP server wiring and MQTT monitoring
  - `videoconfig/`: Resolves the shared configuration directory
  - `api/`: API handlers and middleware
  - `models/`: Data models
  - `service/`: Business logic
- `pkg/`: Public packages that can be used by external projects
- `configs/`: Configuration files
- `scripts/`: Build and deployment scripts
- `test/`: Additional test files

### Running in IDE

```bash
# Build the runnable development binary
go build -o video .

# Run the application (both modules)
go run .

# Run with only one module visible
go run . --no-replays
go run . --no-cameras
```

### Run Modes

```bash
# Build from source and run with this repository's ./video_config directory.
./run-dev.sh

# Run the newest installed Video version, including prereleases, with the configuration files in that version directory.
./run-production.sh
```

`run-production.sh` looks in the platform default `owlcms-video` installation directory. Set `VIDEO_INSTALL_DIR` to use another installation root. Set `VIDEO_DEV_CONFIG_DIR` to use another development configuration directory.

### Configuration-Driven Code

When a feature is driven by configuration, the configuration file and its loader are the single source of truth. Do not add parallel hardcoded fallback logic in the feature code that silently hides missing or incomplete configuration. If defaults are needed, put them in the config loader or embedded default config and make load failures visible.

