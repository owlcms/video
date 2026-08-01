package recording

import "strings"

// ProgressSep is the separator between a progress tag and its payload.
// Using a rare Unicode character so it cannot appear in device names or paths.
const ProgressSep = "¤"
const ProgressDetailSep = "\x1f"

// Progress tags shared by recording and cameras progress reporting.
// Each message is formatted as tag + ProgressSep + payload.
// Callers should use ProgressMsg to build messages and ProgressParse to decode them.
const (
	// ProgLocalSource is emitted while examining a local (USB/V4L2/DirectShow) source.
	// Payload: source name.
	ProgLocalSource = "localSrc"

	// ProgEncoder is emitted while examining a hardware encoder candidate.
	// Payload: encoder name.
	ProgEncoder = "encoder"

	// ProgEncoderUnconfigured is emitted when ffmpeg reports an encoder that has
	// no matching [[encoder]] settings in ffmpeg.toml.
	// Payload: encoder name.
	ProgEncoderUnconfigured = "encNoCfg"

	// ProgListing is emitted when starting a device enumeration pass.
	// Payload: human-readable description (not parsed structurally).
	ProgListing = "listing"

	// ProgHwMsg is emitted for general hardware encoder status messages.
	// Payload: human-readable description (not parsed structurally).
	ProgHwMsg = "hwMsg"

	// ProgRTSPSource is emitted while examining a configured RTSP source.
	// Payload: source name.
	ProgRTSPSource = "rtspSrc"

	// ProgDetectedSource is emitted when a source is accepted into inventory.
	// Payload: source name.
	ProgDetectedSource = "detected"

	// ProgSkippedSource is emitted when a source is filtered out.
	// Payload: source name.
	ProgSkippedSource = "skipped"

	// ProgInventoryReady marks the end of the source inventory build.
	// Payload: human-readable summary.
	ProgInventoryReady = "inventory"

	// ProgCheckingRTSP marks the start of the RTSP inventory phase.
	// Payload: empty.
	ProgCheckingRTSP = "chkRTSP"

	// ProgCheckingLocal marks the start of the local-device inventory phase.
	// Payload: empty.
	ProgCheckingLocal = "chkLocal"

	// ProgCheckingEncoders marks the start of the encoder inventory phase.
	// Payload: empty.
	ProgCheckingEncoders = "chkEnc"

	// ProgPreparing marks a general preparation step.
	// Payload: human-readable description.
	ProgPreparing = "preparing"

	// ProgError reports a top-level error unrelated to an individual source row.
	// Payload: error message.
	ProgError = "error"

	// ProgStreamsAll reports the total number of streams about to start.
	// Payload: count as string.
	ProgStreamsAll = "streamsAll"

	// ProgStreamPrep reports that a stream command is being prepared.
	// Payload: source name.
	ProgStreamPrep = "streamPrep"

	// ProgStreamTest reports that a startup probe is running.
	// Payload: source name.
	ProgStreamTest = "streamTest"

	// ProgValidatePassed reports that a startup probe passed.
	// Payload: source name.
	ProgValidatePassed = "valPass"

	// ProgValidateFailed reports that a startup probe failed.
	// Payload: source name + detail.
	ProgValidateFailed = "valFail"

	// ProgStreamStart reports that a stream process is starting.
	// Payload: source name.
	ProgStreamStart = "streamStart"

	// ProgStreamFailed reports that a stream failed to start.
	// Payload: source name + detail.
	ProgStreamFailed = "streamFail"
)

// ProgressMsg builds a tagged progress message for use with ProbeProgressFunc.
func ProgressMsg(tag, payload string) string {
	return tag + ProgressSep + payload
}

// ProgressDetailPayload combines a display name and detail reason in one payload.
func ProgressDetailPayload(name, detail string) string {
	name = strings.TrimSpace(name)
	detail = strings.TrimSpace(detail)
	if detail == "" {
		return name
	}
	return name + ProgressDetailSep + detail
}

// ProgressPayloadParts splits a progress payload into display name and optional detail.
func ProgressPayloadParts(payload string) (name, detail string) {
	name, detail, _ = strings.Cut(payload, ProgressDetailSep)
	return strings.TrimSpace(name), strings.TrimSpace(detail)
}

// ProgressParse splits a tagged progress message into (tag, payload, ok).
// Returns ok=false if the message does not contain the separator.
func ProgressParse(msg string) (tag, payload string, ok bool) {
	tag, payload, ok = strings.Cut(msg, ProgressSep)
	return
}
