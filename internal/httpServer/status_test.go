package httpServer

import (
	"testing"

	"github.com/owlcms/video/internal/state"
)

func drainStatusChanForTest() {
	for {
		select {
		case <-StatusChan:
		default:
			return
		}
	}
}

func resetStatusForTest(t *testing.T) {
	t.Helper()

	oldStatusMsg := statusMsg
	oldStatusCode := statusCode
	oldLastStatusMessage := lastStatusMessage
	oldVideoReadyReloading := VideoReadyReloading
	oldSession := state.CurrentSession
	oldAthlete := state.CurrentAthlete
	oldLiftType := state.CurrentLiftType
	oldAttempt := state.CurrentAttempt
	drainStatusChanForTest()

	statusMsg = ""
	statusCode = Ready
	lastStatusMessage = StatusMessage{}
	VideoReadyReloading = false
	state.CurrentSession = ""
	state.CurrentAthlete = ""
	state.CurrentLiftType = ""
	state.CurrentAttempt = 0

	t.Cleanup(func() {
		statusMsg = oldStatusMsg
		statusCode = oldStatusCode
		lastStatusMessage = oldLastStatusMessage
		VideoReadyReloading = oldVideoReadyReloading
		state.CurrentSession = oldSession
		state.CurrentAthlete = oldAthlete
		state.CurrentLiftType = oldLiftType
		state.CurrentAttempt = oldAttempt
		drainStatusChanForTest()
	})
}

func TestSendStatusWithDetailsPreservesAttemptMetadata(t *testing.T) {
	resetStatusForTest(t)

	state.CurrentSession = "wrong-session"
	state.CurrentAthlete = "Wrong Athlete"
	state.CurrentLiftType = "WRONG"
	state.CurrentAttempt = 9

	details := StatusAttemptDetails{
		Session:       "3",
		AthleteName:   "SHEPPARD, Ryan",
		LiftType:      "SNATCH",
		AttemptNumber: 2,
	}

	SendStatusWithDetails(Recording, "Recording", details)

	if lastStatusMessage.Code != Recording {
		t.Fatalf("unexpected status code %d", lastStatusMessage.Code)
	}
	if lastStatusMessage.Text != "Recording" {
		t.Fatalf("unexpected status text %q", lastStatusMessage.Text)
	}
	if lastStatusMessage.Session != details.Session {
		t.Fatalf("unexpected session %q", lastStatusMessage.Session)
	}
	if lastStatusMessage.AthleteName != details.AthleteName {
		t.Fatalf("unexpected athlete %q", lastStatusMessage.AthleteName)
	}
	if lastStatusMessage.LiftType != details.LiftType {
		t.Fatalf("unexpected lift type %q", lastStatusMessage.LiftType)
	}
	if lastStatusMessage.AttemptNumber != details.AttemptNumber {
		t.Fatalf("unexpected attempt %d", lastStatusMessage.AttemptNumber)
	}

	select {
	case msg := <-StatusChan:
		if msg.AthleteName != details.AthleteName || msg.LiftType != details.LiftType || msg.AttemptNumber != details.AttemptNumber {
			t.Fatalf("status channel lost details: %+v", msg)
		}
	default:
		t.Fatal("expected status channel message")
	}
}

func TestVideosReadyStatusKeepsAttemptMetadata(t *testing.T) {
	resetStatusForTest(t)

	details := StatusAttemptDetails{
		Session:       "3",
		AthleteName:   "SHEPPARD, Ryan",
		LiftType:      "SNATCH",
		AttemptNumber: 2,
	}

	SendStatusWithDetails(Ready, "Videos ready", details)

	if !VideoReadyReloading {
		t.Fatal("expected video ready reloading flag")
	}
	if lastStatusMessage.Text != "Reloading..." {
		t.Fatalf("unexpected status text %q", lastStatusMessage.Text)
	}
	if lastStatusMessage.AthleteName != details.AthleteName {
		t.Fatalf("unexpected athlete %q", lastStatusMessage.AthleteName)
	}
	if lastStatusMessage.LiftType != details.LiftType {
		t.Fatalf("unexpected lift type %q", lastStatusMessage.LiftType)
	}
	if lastStatusMessage.AttemptNumber != details.AttemptNumber {
		t.Fatalf("unexpected attempt %d", lastStatusMessage.AttemptNumber)
	}
}
