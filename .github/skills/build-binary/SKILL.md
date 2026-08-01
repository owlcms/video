---
name: build-binary
description: 'Use when: building Go commands, validating builds, producing executables, or the user asks to build. In this repo, build the runnable binary with go build -o, not only a plain package build.'
argument-hint: '<command or package to build>'
---

# Build Binary

## When to Use
- The user asks to build, compile, validate a command, or produce an executable.
- You changed code in the root `main` package or any package used by a command binary.
- You need to confirm the runnable program still builds, not just that packages type-check.

## Procedure
1. Identify the command package being built, at the repository root (`.`) in this repo.
2. Build an actual runnable binary with `go build -o <binary-name> .`.
3. On Windows, use the `.exe` suffix for the output binary.
4. Use a plain `go build ./...` or `go build .` only as an additional package check, not as the only build validation.

## Repo Examples
- Video app (the only command): `go build -o video.exe .`

The Cameras and Replays user interfaces live in `internal/cameras` and
`internal/replays`; they are libraries with no `main` and are built as part of
the root `main` package.

## Reporting
When reporting build validation, state the binary command that was run and whether it succeeded.