The user uses Git Bash.

Do not use RunOnMain in Fyne applications - this is outdated and wrong. UI updates are automatically dispatched to the main thread.

When code is config-driven, do not add hardcoded fallback behavior that hides missing or incomplete config. Load the config explicitly, use embedded/default config through the config loader when appropriate, and fail visibly if required config cannot be resolved.
