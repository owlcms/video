package httpServer

import (
	"strings"

	"github.com/owlcms/video/internal/logging"
	"github.com/owlcms/video/internal/state"
)

type StatusCode int

const (
	Ready StatusCode = iota
	Recording
	Trimming
	Error
)

type StatusMessage struct {
	Code          StatusCode `json:"code"`
	Text          string     `json:"text"`
	Session       string     `json:"session"`
	AthleteName   string     `json:"athleteName,omitempty"`
	LiftType      string     `json:"liftType,omitempty"`
	AttemptNumber int        `json:"attemptNumber,omitempty"`
	// Cameras is populated for the Ready message and carries the per-camera
	// publish pointers (videoPath relative to the replays HTTP root,
	// durationMs probed by ffprobe). Lets clients act on a freshly-published
	// clip without a follow-up GET /api/replay-state round-trip.
	Cameras []ReplayCameraState `json:"cameras,omitempty"`
}

type StatusAttemptDetails struct {
	Session       string
	AthleteName   string
	LiftType      string
	AttemptNumber int
}

var (
	StatusChan          = make(chan StatusMessage, 10)
	statusMsg           string
	statusCode          StatusCode
	lastStatusMessage   StatusMessage
	VideoReadyReloading bool
)

func buildStatusMessage(code StatusCode, text string) StatusMessage {
	return buildStatusMessageWithDetails(code, text, StatusAttemptDetails{})
}

func buildStatusMessageWithDetails(code StatusCode, text string, details StatusAttemptDetails) StatusMessage {
	session := details.Session
	if session == "" {
		session = state.CurrentSession
	}
	athleteName := details.AthleteName
	if athleteName == "" {
		athleteName = state.CurrentAthlete
	}
	liftType := details.LiftType
	if liftType == "" {
		liftType = state.CurrentLiftType
	}
	attemptNumber := details.AttemptNumber
	if attemptNumber == 0 {
		attemptNumber = state.CurrentAttempt
	}

	return StatusMessage{
		Code:          code,
		Text:          text,
		Session:       session,
		AthleteName:   athleteName,
		LiftType:      liftType,
		AttemptNumber: attemptNumber,
	}
}

// SendStatus sends a status update to all clients through the broadcast channel
// and updates the Fyne UI through StatusChan
func SendStatus(code StatusCode, text string) {
	SendStatusWithDetails(code, text, StatusAttemptDetails{})
}

// SendStatusWithDetails sends a status update with explicit attempt metadata.
func SendStatusWithDetails(code StatusCode, text string, details StatusAttemptDetails) {
	// Simplify the "Videos ready" message for web display
	VideoReadyReloading = false
	if code == Ready && strings.Contains(text, "Videos ready") {
		text = "Reloading..."
		VideoReadyReloading = true
	}
	msg := buildStatusMessageWithDetails(code, text, details)
	if code == Ready {
		msg.Cameras = snapshotPublishedReplays()
	}
	mu.Lock()
	statusMsg = text
	statusCode = code
	lastStatusMessage = msg
	for client := range clients {
		logging.InfoLogger.Printf("Sending status update: %s", text)
		if err := client.WriteJSON(msg); err != nil {
			logging.ErrorLogger.Printf("Failed to send status: %v", err)
			client.Close()
			delete(clients, client)
			continue
		}
	}
	mu.Unlock()

	// Also send to Fyne UI
	StatusChan <- msg
}
