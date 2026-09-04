// progress.go — alias for the SDK's tool progress event (Story E9).
package message

import sdkmsg "github.com/soulacy/soulacy/sdk/message"

// ProgressEvent is emitted by tool scripts to report sub-step progress.
type ProgressEvent = sdkmsg.ProgressEvent
