package state

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/owlcms/video/internal/logging"
)

var (
	LastStartTime     int64
	LastTimerStopTime int64
	LastDecisionTime  int64

	// New state variables
	CurrentAthlete      string
	CurrentLiftType     string
	CurrentAttempt      int
	StopRequestCount    int
	CurrentCameraNumber int
	CurrentSession      string // Current competition session name
	AvailablePlatforms  []string
)

type StartMessage struct {
	AthleteName   string `json:"athleteName"`
	AttemptNumber int    `json:"attemptNumber"`
	LiftType      string `json:"liftType"`
	Session       string `json:"session"` // Add session field
}

func UpdateStateFromStartMessage(message string) {
	// Find the last space to separate JSON and timestamp
	spaceIndex := strings.LastIndex(message, " ")
	if spaceIndex == -1 {
		return
	}

	jsonPart := message[:spaceIndex]
	timePart := message[spaceIndex+1:]

	// Change to debug level logging
	logging.Trace("Received start message: %s", message)
	logging.Trace("Parsing json: %s", jsonPart)

	var startMsg StartMessage
	err := json.Unmarshal([]byte(jsonPart), &startMsg)
	if err != nil {
		logging.ErrorLogger.Printf("Error parsing start message: %v", err)
		return
	}

	// Change to debug level logging
	logging.Trace("Parsed start message: %+v", startMsg)

	CurrentAthlete = startMsg.AthleteName
	CurrentAttempt = startMsg.AttemptNumber
	CurrentLiftType = startMsg.LiftType
	CurrentSession = startMsg.Session
	CurrentSession = strings.ReplaceAll(CurrentSession, " ", "_")
	LastStartTime = parseTime(timePart)
	StopRequestCount = 0
}

func UpdateStateFromStopMessage(message string) {
	StopRequestCount++
	if StopRequestCount == 1 {
		LastTimerStopTime = time.Now().UnixNano() / int64(time.Millisecond)
		logging.InfoLogger.Println("Stop time recorded")
	}

}

func parseTime(_ string) int64 {
	// Implement the time parsing logic here
	// For now, let's assume it returns a dummy value
	return time.Now().UnixNano() / int64(time.Millisecond)
}
