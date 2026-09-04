package executor

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/soulacy/soulacy/pkg/message"
)

// progressLine is the JSON shape tool scripts write to stdout.
type progressLine struct {
	Type    string `json:"type"`
	Percent int    `json:"percent"`
	Status  string `json:"status"`
}

// ParseProgressLine tries to parse a stdout line as a progress event.
// Returns (event, true) if the line is a valid progress frame; (zero, false) otherwise.
// The line must start with '{' and contain `"type":"progress"`.
func ParseProgressLine(line, runID string) (message.ProgressEvent, bool) {
	line = strings.TrimSpace(line)
	if len(line) == 0 || line[0] != '{' {
		return message.ProgressEvent{}, false
	}
	// Quick string check before the more expensive json.Unmarshal.
	if !strings.Contains(line, `"progress"`) {
		return message.ProgressEvent{}, false
	}
	var pl progressLine
	if err := json.Unmarshal([]byte(line), &pl); err != nil {
		return message.ProgressEvent{}, false
	}
	if pl.Type != "progress" {
		return message.ProgressEvent{}, false
	}
	return message.ProgressEvent{
		RunID:     runID,
		Percent:   pl.Percent,
		Status:    pl.Status,
		Timestamp: time.Now().UTC(),
	}, true
}
