// attachment.go — aliases for the SDK's typed attachment parts (Story E9).
package message

import sdkmsg "github.com/soulacy/soulacy/sdk/message"

// TypedPart is a typed attachment embedded in a message.
type TypedPart = sdkmsg.TypedPart

// Concrete attachment types.
type (
	TextPart     = sdkmsg.TextPart
	FilePart     = sdkmsg.FilePart
	ImagePart    = sdkmsg.ImagePart
	AudioPart    = sdkmsg.AudioPart
	LocationPart = sdkmsg.LocationPart
)

// UnmarshalPartJSON decodes a JSON object with a "kind" field into the
// matching TypedPart implementation.
var UnmarshalPartJSON = sdkmsg.UnmarshalPartJSON
