package message

import "time"

// ProgressEvent is emitted by tool scripts to report sub-step progress.
// Scripts write {"type":"progress","percent":45,"status":"Scanning..."} to stdout.
// The executor parses these lines and publishes them to the queue.
type ProgressEvent struct {
	RunID     string    `json:"run_id"`
	Percent   int       `json:"percent"` // 0-100
	Status    string    `json:"status"`
	Timestamp time.Time `json:"timestamp"`
}
