// Package videoconfig defines the self-contained configuration root used by
// the combined Cameras and Replays application.
package videoconfig

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/owlcms/video/internal/config"
	camerascfg "github.com/owlcms/video/internal/config/cameras"
	ffmpegcfg "github.com/owlcms/video/internal/config/ffmpeg"
	replayscfg "github.com/owlcms/video/internal/config/replays"
)

const (
	DefaultRoot     = config.LocalVideoConfigDir
	CamerasFilename = config.CamerasFilename
	ReplaysFilename = config.ReplaysFilename
	FFmpegFilename  = config.FFmpegFilename
)

// Paths names the three required configuration documents for one video module.
type Paths struct {
	Root    string
	Cameras string
	Replays string
	FFmpeg  string
}

// Resolve returns the named configuration paths below root.
func Resolve(root string) (Paths, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		root = DefaultRoot
	}

	absRoot, err := filepath.Abs(root)
	if err != nil {
		return Paths{}, fmt.Errorf("invalid config directory %q: %w", root, err)
	}

	return Paths{
		Root:    absRoot,
		Cameras: filepath.Join(absRoot, CamerasFilename),
		Replays: filepath.Join(absRoot, ReplaysFilename),
		FFmpeg:  filepath.Join(absRoot, FFmpegFilename),
	}, nil
}

// Validate confirms that a complete video-module configuration is present.
func (p Paths) Validate() error {
	for _, configPath := range []string{p.Cameras, p.Replays, p.FFmpeg} {
		info, err := os.Stat(configPath)
		if err != nil {
			if os.IsNotExist(err) {
				return fmt.Errorf("required configuration file is missing: %s", configPath)
			}
			return fmt.Errorf("cannot access configuration file %s: %w", configPath, err)
		}
		if info.IsDir() {
			return fmt.Errorf("configuration path is a directory: %s", configPath)
		}
	}
	return nil
}

// ExtractDefaults creates missing configuration documents in the module root.
func (p Paths) ExtractDefaults() error {
	if err := os.MkdirAll(p.Root, 0755); err != nil {
		return fmt.Errorf("create config directory %s: %w", p.Root, err)
	}
	if err := camerascfg.ExtractDefaultConfigTo(p.Cameras); err != nil {
		return err
	}
	if err := replayscfg.ExtractDefaultConfig(p.Replays); err != nil {
		return err
	}
	if err := ffmpegcfg.ExtractDefaultConfigTo(p.FFmpeg); err != nil {
		return err
	}
	return nil
}
