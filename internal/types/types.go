package types

// PlatformConfig represents platform-specific configurations
type PlatformConfig struct {
	FfmpegPath   string `toml:"ffmpegPath"`
	FfmpegCamera string `toml:"ffmpegCamera"`
	Format       string `toml:"format"` // Renamed from ffmpegFormat
	Params       string `toml:"params"` // Renamed from ffmpegParams
	Size         string `toml:"size"`
	Fps          int    `toml:"fps"`
}
