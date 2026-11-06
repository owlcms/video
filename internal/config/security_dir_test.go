package config

import (
	"path/filepath"
	"testing"
)

func TestSecurityDir(t *testing.T) {
	original := ConfigDir
	t.Cleanup(func() { ConfigDir = original })
	root := t.TempDir()
	ConfigDir = filepath.Join(root, "owlcms-video", "version")
	t.Setenv(SecurityDirEnv, "")
	t.Setenv(ControlPanelDirEnv, "")
	t.Setenv(SharedConfigDirEnv, filepath.Join(root, "ffmpeg"))

	if got := SecurityDir(); got != ConfigDir {
		t.Fatalf("standalone: got %q, want %q", got, ConfigDir)
	}
	t.Setenv(ControlPanelDirEnv, root)
	if got, want := SecurityDir(), filepath.Dir(ConfigDir); got != want {
		t.Fatalf("control panel: got %q, want %q", got, want)
	}
	override := filepath.Join(root, "security")
	t.Setenv(SecurityDirEnv, override)
	if got := SecurityDir(); got != override {
		t.Fatalf("override: got %q, want %q", got, override)
	}
}
