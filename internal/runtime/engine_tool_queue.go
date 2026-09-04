package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
)

const (
	agentQueueDefaultTTL   = 24 * time.Hour
	agentQueueMaxTTL       = 7 * 24 * time.Hour
	agentQueueMaxQueues    = 256
	agentQueueMaxItems     = 1000
	agentQueueMaxItemBytes = 64 * 1024
	agentQueueDefaultName  = "default"
)

var agentQueueNameRE = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

type agentQueueItem struct {
	ID        string          `json:"id"`
	Queue     string          `json:"queue"`
	Item      json.RawMessage `json:"item"`
	CreatedAt time.Time       `json:"created_at"`
	ExpiresAt time.Time       `json:"expires_at"`
}

// QueueItem is the public view of an item held in Soulacy's ephemeral
// in-memory queue store. The queue store is intentionally process-local and
// reset on restart; it is for live workflow handoffs, not durable storage.
type QueueItem = agentQueueItem

type agentQueueStore struct {
	mu     sync.Mutex
	queues map[string][]agentQueueItem
}

func newAgentQueueStore() *agentQueueStore {
	return &agentQueueStore{queues: map[string][]agentQueueItem{}}
}

func (s *agentQueueStore) create(queue string) (bool, error) {
	if !agentQueueNameRE.MatchString(queue) {
		return false, fmt.Errorf("queue name must be 1-128 characters and contain only letters, numbers, dot, underscore, colon, or dash")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked(time.Now().UTC())
	if _, ok := s.queues[queue]; ok {
		return false, nil
	}
	if len(s.queues) >= agentQueueMaxQueues {
		return false, fmt.Errorf("queue limit reached: max %d queues", agentQueueMaxQueues)
	}
	s.queues[queue] = nil
	return true, nil
}

func (s *agentQueueStore) names() []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked(time.Now().UTC())
	names := make([]string, 0, len(s.queues))
	for name := range s.queues {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]map[string]any, 0, len(names))
	for _, name := range names {
		out = append(out, map[string]any{
			"queue": name,
			"count": len(s.queues[name]),
		})
	}
	return out
}

func (s *agentQueueStore) put(queue string, raw json.RawMessage, ttl time.Duration) (agentQueueItem, error) {
	if queue == "" {
		queue = agentQueueDefaultName
	}
	if !agentQueueNameRE.MatchString(queue) {
		return agentQueueItem{}, fmt.Errorf("queue name must be 1-128 characters and contain only letters, numbers, dot, underscore, colon, or dash")
	}
	if len(raw) == 0 {
		raw = json.RawMessage(`null`)
	}
	if len(raw) > agentQueueMaxItemBytes {
		return agentQueueItem{}, fmt.Errorf("queue item too large: %d bytes exceeds %d byte limit", len(raw), agentQueueMaxItemBytes)
	}
	if ttl <= 0 {
		ttl = agentQueueDefaultTTL
	}
	if ttl > agentQueueMaxTTL {
		ttl = agentQueueMaxTTL
	}

	now := time.Now().UTC()
	item := agentQueueItem{
		ID:        uuid.NewString(),
		Queue:     queue,
		Item:      append(json.RawMessage(nil), raw...),
		CreatedAt: now,
		ExpiresAt: now.Add(ttl),
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked(now)
	if _, ok := s.queues[queue]; !ok && len(s.queues) >= agentQueueMaxQueues {
		return agentQueueItem{}, fmt.Errorf("queue limit reached: max %d queues", agentQueueMaxQueues)
	}
	q := append(s.queues[queue], item)
	if len(q) > agentQueueMaxItems {
		q = q[len(q)-agentQueueMaxItems:]
	}
	s.queues[queue] = q
	return item, nil
}

func (s *agentQueueStore) list(queue string, limit int) ([]agentQueueItem, error) {
	if queue == "" {
		queue = agentQueueDefaultName
	}
	if !agentQueueNameRE.MatchString(queue) {
		return nil, fmt.Errorf("queue name must be 1-128 characters and contain only letters, numbers, dot, underscore, colon, or dash")
	}
	if limit <= 0 || limit > 100 {
		limit = 25
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked(time.Now().UTC())
	items := append([]agentQueueItem(nil), s.queues[queue]...)
	if len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}

func (s *agentQueueStore) take(queue string) (agentQueueItem, bool, error) {
	if queue == "" {
		queue = agentQueueDefaultName
	}
	if !agentQueueNameRE.MatchString(queue) {
		return agentQueueItem{}, false, fmt.Errorf("queue name must be 1-128 characters and contain only letters, numbers, dot, underscore, colon, or dash")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked(time.Now().UTC())
	q := s.queues[queue]
	if len(q) == 0 {
		return agentQueueItem{}, false, nil
	}
	item := q[0]
	q = q[1:]
	if len(q) == 0 {
		delete(s.queues, queue)
	} else {
		s.queues[queue] = q
	}
	return item, true, nil
}

func (s *agentQueueStore) clear(queue string) (int, error) {
	if queue == "" {
		queue = agentQueueDefaultName
	}
	if !agentQueueNameRE.MatchString(queue) {
		return 0, fmt.Errorf("queue name must be 1-128 characters and contain only letters, numbers, dot, underscore, colon, or dash")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	n := len(s.queues[queue])
	delete(s.queues, queue)
	return n, nil
}

// QueueCreate creates a named ephemeral queue. queue_put auto-creates queues,
// but the API form is useful for preflight, MCP clients, and GUI inspection.
func (e *Engine) QueueCreate(queue string) (bool, error) {
	if e.queueStore == nil {
		e.queueStore = newAgentQueueStore()
	}
	if queue == "" {
		queue = agentQueueDefaultName
	}
	return e.queueStore.create(queue)
}

// QueueNames returns all known ephemeral queue names and item counts.
func (e *Engine) QueueNames() []map[string]any {
	if e.queueStore == nil {
		e.queueStore = newAgentQueueStore()
	}
	return e.queueStore.names()
}

// QueuePut stores a JSON value in an ephemeral queue.
func (e *Engine) QueuePut(queue string, raw json.RawMessage, ttl time.Duration) (QueueItem, error) {
	if e.queueStore == nil {
		e.queueStore = newAgentQueueStore()
	}
	return e.queueStore.put(queue, raw, ttl)
}

// QueueList lists recent queue items without removing them.
func (e *Engine) QueueList(queue string, limit int) ([]QueueItem, error) {
	if e.queueStore == nil {
		e.queueStore = newAgentQueueStore()
	}
	return e.queueStore.list(queue, limit)
}

// QueueTake removes and returns the oldest queue item.
func (e *Engine) QueueTake(queue string) (QueueItem, bool, error) {
	if e.queueStore == nil {
		e.queueStore = newAgentQueueStore()
	}
	return e.queueStore.take(queue)
}

// QueueClear deletes all items from a queue.
func (e *Engine) QueueClear(queue string) (int, error) {
	if e.queueStore == nil {
		e.queueStore = newAgentQueueStore()
	}
	return e.queueStore.clear(queue)
}

func (s *agentQueueStore) sweepLocked(now time.Time) {
	for name, q := range s.queues {
		kept := q[:0]
		for _, item := range q {
			if item.ExpiresAt.After(now) {
				kept = append(kept, item)
			}
		}
		if len(kept) == 0 {
			delete(s.queues, name)
			continue
		}
		s.queues[name] = kept
	}
}

func (e *Engine) buildQueueBuiltins() []BuiltinTool {
	if e.queueStore == nil {
		e.queueStore = newAgentQueueStore()
	}
	return []BuiltinTool{
		{
			Name:        "queue_create",
			Gate:        "",
			Description: "Create a named in-memory queue for temporary Soulacy workflow state. queue_put also auto-creates queues, but use this when a workflow should declare the queue up front.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"queue": map[string]any{
						"type":        "string",
						"description": "Queue name, e.g. drafts, pending_docs, weather_jobs. Defaults to default. Use letters, numbers, dot, underscore, colon, or dash.",
					},
				},
			},
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				queueName := queueArg(args)
				created, err := e.queueStore.create(queueName)
				if err != nil {
					return "", fmt.Errorf("queue_create: %w", err)
				}
				return queueResult(map[string]any{
					"ok":      true,
					"queue":   queueName,
					"created": created,
				}), nil
			},
		},
		{
			Name:        "queue_names",
			Gate:        "",
			Description: "List named in-memory queues currently known to this Soulacy gateway process, including item counts. Queues are ephemeral and reset on restart.",
			Parameters: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				queues := e.queueStore.names()
				return queueResult(map[string]any{
					"ok":     true,
					"count":  len(queues),
					"queues": queues,
				}), nil
			},
		},
		{
			Name:        "queue_put",
			Gate:        "",
			Description: "Store a JSON value in Soulacy's in-memory ephemeral queue for later workflow steps. Use instead of write_file for temporary handoffs. Data is not persisted across restarts.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"queue": map[string]any{
						"type":        "string",
						"description": "Queue name, e.g. drafts, pending_docs, weather_jobs. Defaults to default if omitted.",
					},
					"item": map[string]any{
						"description": "Any JSON-serializable value to enqueue",
					},
					"ttl_seconds": map[string]any{
						"type":        "integer",
						"description": "Optional time-to-live in seconds. Defaults to 24 hours, max 7 days.",
					},
				},
				"required": []string{"item"},
			},
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				queueName := queueArg(args)
				raw, err := json.Marshal(args["item"])
				if err != nil {
					return "", fmt.Errorf("queue_put: item is not JSON-serializable: %w", err)
				}
				item, err := e.queueStore.put(queueName, raw, time.Duration(argInt(args, "ttl_seconds", 0))*time.Second)
				if err != nil {
					return "", fmt.Errorf("queue_put: %w", err)
				}
				return queueResult(map[string]any{
					"ok":         true,
					"id":         item.ID,
					"queue":      item.Queue,
					"expires_at": item.ExpiresAt,
				}), nil
			},
		},
		{
			Name:        "queue_take",
			Gate:        "",
			Description: "Take and remove the oldest item from a Soulacy in-memory queue. Returns {ok:false, empty:true} if no item is available.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"queue": map[string]any{
						"type":        "string",
						"description": "Queue name to read from. Defaults to default if omitted.",
					},
				},
			},
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				item, ok, err := e.queueStore.take(queueArg(args))
				if err != nil {
					return "", fmt.Errorf("queue_take: %w", err)
				}
				if !ok {
					return queueResult(map[string]any{"ok": false, "empty": true}), nil
				}
				return queueResult(map[string]any{
					"ok":         true,
					"id":         item.ID,
					"queue":      item.Queue,
					"item":       json.RawMessage(item.Item),
					"created_at": item.CreatedAt,
					"expires_at": item.ExpiresAt,
				}), nil
			},
		},
		{
			Name:        "queue_list",
			Gate:        "",
			Description: "List recent items in a Soulacy in-memory queue without removing them. Use for inspection or downstream batch processing.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"queue": map[string]any{
						"type":        "string",
						"description": "Queue name to inspect. Defaults to default if omitted.",
					},
					"limit": map[string]any{
						"type":        "integer",
						"description": "Maximum items to return, default 25, max 100",
					},
				},
			},
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				items, err := e.queueStore.list(queueArg(args), argInt(args, "limit", 25))
				if err != nil {
					return "", fmt.Errorf("queue_list: %w", err)
				}
				return queueResult(map[string]any{
					"ok":    true,
					"count": len(items),
					"items": items,
				}), nil
			},
		},
		{
			Name:        "queue_clear",
			Gate:        "",
			Description: "Clear all items from a Soulacy in-memory queue. Use only when the temporary work queue is no longer needed.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"queue": map[string]any{
						"type":        "string",
						"description": "Queue name to clear. Defaults to default if omitted.",
					},
				},
			},
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				n, err := e.queueStore.clear(queueArg(args))
				if err != nil {
					return "", fmt.Errorf("queue_clear: %w", err)
				}
				return queueResult(map[string]any{"ok": true, "cleared": n}), nil
			},
		},
	}
}

func queueResult(v map[string]any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func queueArg(args map[string]any) string {
	if v := argString(args, "queue"); v != "" {
		return v
	}
	if v := argString(args, "name"); v != "" {
		return v
	}
	if v := argString(args, "queue_name"); v != "" {
		return v
	}
	return agentQueueDefaultName
}
