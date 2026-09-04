// Package extstorage defines the External Storage Protocol v1 (Story E24):
// JSON-RPC 2.0 messages, one per line (NDJSON), over a sidecar's
// stdin/stdout. It extends the sidecar pattern of sdk/extchannel (ECP, E3)
// to vector / queue / storage backends so third-party database drivers can
// be configured at runtime without recompiling the gateway.
//
// Lifecycle: the HOST opens with a "negotiate" request carrying its
// protocol version, a host name, and the absolute path of a per-run shared
// scratch directory. The sidecar responds with min(host, sidecar) protocol
// and echoes the shared dir to prove it parsed the contract. Backend calls
// follow (vector.*, queue.*); "shutdown" asks the sidecar to exit (≤5s).
//
// Shared mounts: large documents and media move as FILES under the shared
// scratch directory — method params reference them by path relative to
// that directory — never as inline JSON payloads. The host creates the
// directory before spawn and owns its lifecycle (mirrors the E13
// staging-dir pattern and E1 resource semantics: the reference travels on
// the wire, the bytes travel on disk).
//
// Compatibility: structs grow by APPENDING fields only; unknown methods
// must be answered with error code -32601, never a crash; notifications
// with unknown methods are skipped. See docs/EXTERNAL_STORAGE_PROTOCOL.md
// and the SDK README.
package extstorage

import (
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/soulacy/soulacy/sdk/memory"
)

// ProtocolVersion is the host's current External Storage Protocol version.
// Negotiation picks min(host, sidecar).
const ProtocolVersion = 1

// Version2 is the JSON-RPC version string every message must carry.
const Version2 = "2.0"

// Method names. Vector methods mirror sdk/vector.Backend; queue methods
// mirror sdk/queue.Backend (deliveries arrive as queue.message
// NOTIFICATIONS from the sidecar, acked back by the host).
const (
	MethodNegotiate = "negotiate"
	MethodShutdown  = "shutdown"

	MethodVectorWrite  = "vector.write"
	MethodVectorSearch = "vector.search"

	MethodQueuePublish     = "queue.publish"
	MethodQueueSubscribe   = "queue.subscribe"
	MethodQueueUnsubscribe = "queue.unsubscribe"
	MethodQueueAck         = "queue.ack"

	// NotifyQueueMessage is sent sidecar→host (no id) to deliver one
	// queued message to an active subscription.
	NotifyQueueMessage = "queue.message"

	MethodStorageArchive     = "storage.archive"
	MethodStorageSearch      = "storage.search"
	MethodStorageReadByScope = "storage.read_by_scope"
	MethodStorageReadGlobal  = "storage.read_global"
	MethodStoragePrune       = "storage.prune"
)

// JSON-RPC 2.0 error codes (the standard set plus protocol-specific ones).
const (
	CodeParseError     = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternalError  = -32603

	// CodeBackendError reports a backend-side operational failure
	// (connection lost, disk full, …) for a well-formed request.
	CodeBackendError = -32000
)

// Error is a JSON-RPC 2.0 error object.
type Error struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *Error) Error() string {
	return fmt.Sprintf("extstorage: rpc error %d: %s", e.Code, e.Message)
}

// Message is the superset of JSON-RPC request, response, and notification.
//   - request:      ID + Method (Params optional)
//   - notification: Method, no ID
//   - response:     ID + (Result or Error), no Method
type Message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

// IsRequest reports whether m is a request (has both an id and a method).
func (m Message) IsRequest() bool { return m.ID != nil && m.Method != "" }

// IsNotification reports whether m is a notification (method, no id).
func (m Message) IsNotification() bool { return m.ID == nil && m.Method != "" }

// IsResponse reports whether m is a response (id, no method).
func (m Message) IsResponse() bool { return m.ID != nil && m.Method == "" }

// ParseMessage decodes one NDJSON line. A message must carry the JSON-RPC
// 2.0 version marker and at least a method or an id. Unknown METHODS are
// not an error here — dispatch layers answer requests with
// CodeMethodNotFound and skip unknown notifications (forward compat).
func ParseMessage(line []byte) (Message, error) {
	var m Message
	if err := json.Unmarshal(line, &m); err != nil {
		return Message{}, fmt.Errorf("extstorage: invalid message: %w", err)
	}
	if m.JSONRPC != Version2 {
		return Message{}, fmt.Errorf("extstorage: message missing jsonrpc %q marker", Version2)
	}
	if m.ID == nil && m.Method == "" {
		return Message{}, fmt.Errorf("extstorage: message has neither id nor method")
	}
	return m, nil
}

// WriteMessage encodes a message as exactly one NDJSON line.
func WriteMessage(w io.Writer, m Message) error {
	if m.JSONRPC == "" {
		m.JSONRPC = Version2
	}
	data, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("extstorage: marshal message: %w", err)
	}
	_, err = w.Write(append(data, '\n'))
	return err
}

// MustParams marshals v for a Params/Result field, panicking on failure —
// for static, test, and constant payloads only.
func MustParams(v any) json.RawMessage {
	raw, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("extstorage: marshal params: %v", err))
	}
	return raw
}

// NewRequest builds a request message with marshalled params.
func NewRequest(id int64, method string, params any) (Message, error) {
	raw, err := json.Marshal(params)
	if err != nil {
		return Message{}, fmt.Errorf("extstorage: marshal params: %w", err)
	}
	return Message{JSONRPC: Version2, ID: &id, Method: method, Params: raw}, nil
}

// NewResponse builds a success response with a marshalled result.
func NewResponse(id int64, result any) (Message, error) {
	raw, err := json.Marshal(result)
	if err != nil {
		return Message{}, fmt.Errorf("extstorage: marshal result: %w", err)
	}
	return Message{JSONRPC: Version2, ID: &id, Result: raw}, nil
}

// NewErrorResponse builds an error response.
func NewErrorResponse(id int64, code int, msg string) Message {
	return Message{JSONRPC: Version2, ID: &id, Error: &Error{Code: code, Message: msg}}
}

// NegotiateParams opens the session (host → sidecar request params).
type NegotiateParams struct {
	// Protocol is the caller's protocol version (≥1).
	Protocol int `json:"protocol"`
	// Name identifies the host (in requests) / sidecar (in results).
	Name string `json:"name"`
	// Capabilities advertises which backend families the peer supports,
	// e.g. ["vector"], ["queue"], ["vector","queue"].
	Capabilities []string `json:"capabilities,omitempty"`
	// SharedDir is the ABSOLUTE path of the per-run scratch directory the
	// host created for this sidecar. Large payloads move as files under
	// it; method params reference them by relative path.
	SharedDir string `json:"shared_dir,omitempty"`
}

// NegotiateResult is the sidecar's reply.
type NegotiateResult struct {
	// Protocol is min(host, sidecar).
	Protocol int `json:"protocol"`
	// Name identifies the sidecar implementation.
	Name string `json:"name,omitempty"`
	// Capabilities lists the backend families the sidecar implements.
	Capabilities []string `json:"capabilities,omitempty"`
	// SharedDir echoes NegotiateParams.SharedDir, proving the sidecar
	// parsed the shared-mount contract.
	SharedDir string `json:"shared_dir,omitempty"`
}

// Negotiate validates the peer's negotiate payload and returns the
// protocol version both sides will speak: min(ProtocolVersion, peer).
func Negotiate(p NegotiateParams) (int, error) {
	if p.Protocol <= 0 {
		return 0, fmt.Errorf("extstorage: negotiate missing protocol version")
	}
	if p.Name == "" {
		return 0, fmt.Errorf("extstorage: negotiate missing peer name")
	}
	v := p.Protocol
	if v > ProtocolVersion {
		v = ProtocolVersion
	}
	return v, nil
}

// VectorWriteParams mirrors vector.Backend.Write. The entry travels
// inline; oversized Content should be spilled to a SharedDir file and
// referenced via ContentFile instead (one of Content/ContentFile).
type VectorWriteParams struct {
	ID        string  `json:"id"`
	AgentID   string  `json:"agent_id"`
	SessionID string  `json:"session_id,omitempty"`
	Scope     string  `json:"scope,omitempty"`
	Content   string  `json:"content,omitempty"`
	Timestamp int64   `json:"timestamp,omitempty"` // unix seconds
	Relevance float64 `json:"relevance,omitempty"`

	// ContentFile is a path RELATIVE to the negotiated SharedDir holding
	// the content bytes (shared-mount transport for large payloads).
	ContentFile string `json:"content_file,omitempty"`
}

// VectorWriteResult acknowledges a write.
type VectorWriteResult struct {
	OK bool `json:"ok"`
}

// VectorSearchParams mirrors vector.Backend.Search.
type VectorSearchParams struct {
	AgentID string `json:"agent_id,omitempty"` // empty = all agents
	Query   string `json:"query"`
	TopK    int    `json:"top_k"`
}

// VectorHit is one search result.
type VectorHit struct {
	ID          string  `json:"id"`
	AgentID     string  `json:"agent_id,omitempty"`
	SessionID   string  `json:"session_id,omitempty"`
	Scope       string  `json:"scope,omitempty"`
	Content     string  `json:"content,omitempty"`
	Timestamp   int64   `json:"timestamp,omitempty"`
	Distance    float64 `json:"distance"`
	ContentFile string  `json:"content_file,omitempty"`
}

// VectorSearchResult carries the hits, most similar first.
type VectorSearchResult struct {
	Results []VectorHit `json:"results"`
}

// QueuePublishParams mirrors queue.Backend.Publish. Data is base64 (Go's
// encoding/json []byte convention).
type QueuePublishParams struct {
	Subject string `json:"subject"`
	Data    []byte `json:"data"`
}

// QueuePublishResult acknowledges a publish.
type QueuePublishResult struct {
	OK bool `json:"ok"`
}

// QueueSubscribeParams mirrors queue.Backend.Subscribe.
type QueueSubscribeParams struct {
	Subject string `json:"subject"`
	Group   string `json:"group,omitempty"`
}

// QueueSubscribeResult returns the sidecar-assigned subscription handle.
type QueueSubscribeResult struct {
	SubscriptionID string `json:"subscription_id"`
}

// QueueUnsubscribeParams cancels a subscription.
type QueueUnsubscribeParams struct {
	SubscriptionID string `json:"subscription_id"`
}

// QueueMessageParams is the payload of a queue.message NOTIFICATION
// (sidecar → host): one delivery to an active subscription.
type QueueMessageParams struct {
	SubscriptionID string `json:"subscription_id"`
	Subject        string `json:"subject"`
	Data           []byte `json:"data"`
	// DeliveryID identifies this delivery for queue.ack. Empty means the
	// backend doesn't track acks (at-most-once).
	DeliveryID string `json:"delivery_id,omitempty"`
}

// QueueAckParams acknowledges one delivery (host → sidecar).
type QueueAckParams struct {
	DeliveryID string `json:"delivery_id"`
}

// StorageArchiveParams is the payload for storage.archive (host → sidecar).
type StorageArchiveParams struct {
	Entry       memory.Entry `json:"entry"`
	ContentFile string       `json:"content_file,omitempty"`
}

// StorageArchiveResult acknowledges a storage archive write.
type StorageArchiveResult struct {
	OK bool `json:"ok"`
}

// StorageSearchParams is the query for storage.search.
type StorageSearchParams struct {
	AgentID string `json:"agent_id"`
	Query   string `json:"query"`
	Limit   int    `json:"limit"`
}

// StorageSearchResult returns FTS/substring matches.
type StorageSearchResult struct {
	Entries []memory.Entry `json:"entries"`
}

// StorageReadByScopeParams lists scoped entries.
type StorageReadByScopeParams struct {
	AgentID   string       `json:"agent_id"`
	SessionID string       `json:"session_id"`
	Scope     memory.Scope `json:"scope"`
	Limit     int          `json:"limit"`
}

// StorageReadByScopeResult returns the scoped entries.
type StorageReadByScopeResult struct {
	Entries []memory.Entry `json:"entries"`
}

// StorageReadGlobalParams lists global entries.
type StorageReadGlobalParams struct {
	AgentID string `json:"agent_id"`
	Limit   int    `json:"limit"`
}

// StorageReadGlobalResult returns the global entries.
type StorageReadGlobalResult struct {
	Entries []memory.Entry `json:"entries"`
}

// StoragePruneParams deletes old records.
type StoragePruneParams struct {
	AgentID string    `json:"agent_id"`
	Before  time.Time `json:"before"`
}

// StoragePruneResult returns how many records were deleted.
type StoragePruneResult struct {
	RowsDeleted int64 `json:"rows_deleted"`
}
