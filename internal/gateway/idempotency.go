package gateway

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/soulacy/soulacy/internal/apiversion"
	"github.com/soulacy/soulacy/internal/concurrency"
	"github.com/soulacy/soulacy/pkg/agent"
)

// idempotencyTTL bounds how long a replay stays available. Long enough to
// cover a client retrying through a network partition or a CI step rerun;
// short enough that the cache cannot grow without bound.
const idempotencyTTL = 24 * time.Hour

// idempotencyLimit caps the number of retained records. A mutation cache that
// can be grown without limit by an authenticated client is a memory-pressure
// lever, so eviction is by oldest-first once the cap is reached.
const idempotencyLimit = 4096

type idempotencyRecord struct {
	status      int
	body        []byte
	contentType string
	requestHash string
	requestID   string
	storedAt    time.Time
	inFlight    bool
}

// idempotencyStore replays the response of a completed mutation when the same
// key is presented again. Keys are namespaced by workspace and route, so one
// tenant's key can never collide with another's, and a key reused on a
// different route is a different key rather than a silent cross-wire.
type idempotencyStore struct {
	mu      sync.Mutex
	records map[string]*idempotencyRecord
	now     func() time.Time
}

func newIdempotencyStore() *idempotencyStore {
	return &idempotencyStore{records: map[string]*idempotencyRecord{}, now: time.Now}
}

func (s *idempotencyStore) key(workspaceID, method, route, clientKey string) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{workspaceID, method, route, clientKey}, "\x00")))
	return hex.EncodeToString(sum[:])
}

func (s *idempotencyStore) evictLocked() {
	if len(s.records) <= idempotencyLimit {
		return
	}
	oldestKey, oldestAt := "", time.Time{}
	for key, record := range s.records {
		if oldestAt.IsZero() || record.storedAt.Before(oldestAt) {
			oldestKey, oldestAt = key, record.storedAt
		}
	}
	if oldestKey != "" {
		delete(s.records, oldestKey)
	}
}

// begin reserves a key. It returns a replayable record when the same request
// already completed, and reports inFlight when an identical request is still
// running — a concurrent duplicate must not be executed twice.
func (s *idempotencyStore) begin(key, requestHash, requestID string) (*idempotencyRecord, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.records[key]; ok {
		if s.now().Sub(existing.storedAt) > idempotencyTTL {
			delete(s.records, key)
		} else if existing.requestHash != requestHash {
			// Same key, different payload. Replaying the first response would
			// silently discard the second request; executing it would break
			// the promise the key makes. Refusing is the only honest option.
			return nil, false, &apiversion.IncompatibleError{
				Code:    apiversion.CodeIdempotencyReus,
				Message: "this idempotency key was already used with a different request body",
				Remedy:  "use a fresh idempotency key for a different request",
			}
		} else if existing.inFlight {
			return nil, false, &apiversion.IncompatibleError{
				Code:    apiversion.CodeIdempotencyBusy,
				Message: "an identical request with this idempotency key is still in progress",
				Remedy:  "wait for the original request to complete, then retry",
			}
		} else {
			return existing, true, nil
		}
	}
	s.evictLocked()
	s.records[key] = &idempotencyRecord{requestHash: requestHash, requestID: requestID, storedAt: s.now(), inFlight: true}
	return nil, false, nil
}

func (s *idempotencyStore) complete(key string, status int, contentType string, body []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[key]
	if !ok {
		return
	}
	// Only successful mutations are replayable. Caching a 500 would turn a
	// transient failure into a permanent one for the lifetime of the key.
	if status >= 200 && status < 300 {
		record.status, record.contentType = status, contentType
		record.body = append([]byte(nil), body...)
		record.inFlight = false
		record.storedAt = s.now()
		return
	}
	delete(s.records, key)
}

// abandon releases a reservation whose handler never produced a response.
func (s *idempotencyStore) abandon(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if record, ok := s.records[key]; ok && record.inFlight {
		delete(s.records, key)
	}
}

// idempotencyMW makes retried mutations safe. It is opt-in per request: a
// client that sends no Idempotency-Key gets exactly today's behaviour, so
// nothing existing changes shape.
func (s *Server) idempotencyMW() fiber.Handler {
	return func(c *fiber.Ctx) error {
		switch c.Method() {
		case fiber.MethodPost, fiber.MethodPut, fiber.MethodPatch, fiber.MethodDelete:
		default:
			return c.Next()
		}
		clientKey := strings.TrimSpace(c.Get("Idempotency-Key"))
		if clientKey == "" || s.idempotency == nil {
			return c.Next()
		}
		workspaceID := ""
		if identity, ok := requestIdentity(c); ok {
			workspaceID = identity.WorkspaceID()
		}
		route := c.Route().Path
		if route == "" {
			route = c.Path()
		}
		bodySum := sha256.Sum256(c.Body())
		key := s.idempotency.key(workspaceID, c.Method(), route, clientKey)
		requestID := localString(c.Locals("request_id"))

		replay, found, err := s.idempotency.begin(key, hex.EncodeToString(bodySum[:]), requestID)
		if err != nil {
			var typed *apiversion.IncompatibleError
			if ok := asIncompatible(err, &typed); ok {
				status := fiber.StatusConflict
				return c.Status(status).JSON(fiber.Map{"error": typed.Message, "code": typed.Code, "remedy": typed.Remedy})
			}
			return s.errMsg(c, fiber.StatusConflict, "idempotency key conflict")
		}
		if found {
			c.Set("Idempotency-Replayed", "true")
			c.Set("X-Soulacy-Original-Request-Id", replay.requestID)
			if replay.contentType != "" {
				c.Set(fiber.HeaderContentType, replay.contentType)
			}
			return c.Status(replay.status).Send(replay.body)
		}

		if err := c.Next(); err != nil {
			s.idempotency.abandon(key)
			return err
		}
		s.idempotency.complete(key, c.Response().StatusCode(), string(c.Response().Header.ContentType()), c.Response().Body())
		return nil
	}
}

func asIncompatible(err error, target **apiversion.IncompatibleError) bool {
	typed, ok := err.(*apiversion.IncompatibleError)
	if ok {
		*target = typed
	}
	return ok
}

// resourceETag derives a strong validator from a resource's current state.
// Hashing the representation means callers do not have to thread a version
// column through every store before optimistic concurrency works.
func resourceETag(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(encoded)
	return `"` + hex.EncodeToString(sum[:16]) + `"`
}

// checkIfMatch enforces optimistic concurrency (MU-028).
//
// The decision lives in internal/concurrency; this function is the HTTP shell
// around it. Two implementations of "is this write stale" is how one of them
// ends up missing a case — the same reasoning that collapsed the approval
// eligibility check in MU-022 into one function.
//
// It returns rejected=true when it has already written the 409 response, so
// the caller must return immediately. The boolean is not redundant with the
// error: Fiber's Status().JSON() returns nil on success, so a handler that
// only checked the error would sail straight past a rejected write and apply
// the change anyway.
func (s *Server) checkIfMatch(c *fiber.Ctx, current any) (rejected bool, err error) {
	etag := resourceETag(current)
	c.Set(fiber.HeaderETag, etag)

	// A comma-separated If-Match list is legal HTTP; any member matching is a
	// match. Resolved here rather than inside the policy, which reasons about
	// one token.
	supplied := strings.TrimSpace(c.Get(fiber.HeaderIfMatch))
	precondition := concurrency.Precondition{
		Value: firstMatching(supplied, etag),
		// Required in Team and Scale. Letting a missing precondition through
		// makes concurrency control opt-in, and the client that forgets is
		// exactly the one that overwrites silently every time — which is the
		// bug. A personal deployment has nobody to conflict with and is
		// unchanged (product invariant 7).
		RequireMatch: s.authorizationRequired(),
	}

	switch checkErr := precondition.Check(etag); {
	case checkErr == nil:
		return false, nil
	case errors.Is(checkErr, concurrency.ErrPreconditionRequired):
		// 428, not 409. "You did not send a version" and "your version is
		// stale" have different remedies, and a client told 409 will re-read
		// and retry — succeeding, and still not sending a precondition.
		return true, c.Status(fiber.StatusPreconditionRequired).JSON(fiber.Map{
			"error":  "this update requires the current version",
			"code":   apiversion.CodeStaleWrite,
			"remedy": "GET the resource to obtain its ETag, then send it in If-Match",
			"etag":   etag,
		})
	default:
		conflict := concurrency.NewConflict(
			resourceLabel(c), supplied, etag, lastModifiedBy(current), lastModifiedAt(current))
		return true, c.Status(fiber.StatusConflict).JSON(fiber.Map{
			"error":            conflict.Message,
			"code":             apiversion.CodeStaleWrite,
			"remedy":           "GET the resource to obtain its current ETag, then retry with that value in If-Match",
			"etag":             etag,
			"current_version":  conflict.CurrentVersion,
			"supplied_version": conflict.SuppliedVersion,
			"last_modified_by": conflict.LastModifiedBy,
			"last_modified_at": conflict.LastModifiedAt,
		})
	}
}

// firstMatching reduces an If-Match list to the member that matches, or the
// first member when none does — so a rejection echoes something the client
// recognises rather than the whole header.
func firstMatching(supplied, current string) string {
	if strings.TrimSpace(supplied) == "" {
		return ""
	}
	candidates := strings.Split(supplied, ",")
	for _, candidate := range candidates {
		if concurrency.Match(candidate, current) {
			return strings.TrimSpace(candidate)
		}
	}
	if strings.TrimSpace(candidates[0]) == concurrency.Wildcard {
		return concurrency.Wildcard
	}
	return strings.TrimSpace(candidates[0])
}

// resourceLabel names what conflicted, for a message a person can act on.
func resourceLabel(c *fiber.Ctx) string {
	if id := strings.TrimSpace(c.Params("id")); id != "" {
		return id
	}
	return "this resource"
}

// lastModifiedBy names the other actor, so the loser of a conflict learns who
// to talk to (MU-028 criterion 5).
//
// Empty today, and deliberately not faked. agent.Definition carries no
// last-editor field: the actor is passed to Loader.UpsertInWorkspace and
// recorded in the audit trail, which is a separate lookup this synchronous
// path should not take on every conflict. Conflict handles the unattributed
// case — the message reads "changed since you loaded it" rather than naming
// nobody — so the gap degrades the message rather than producing a wrong one.
// Closing it means reading the agent audit history here, which belongs with
// the rest of criterion 5.
func lastModifiedBy(current any) string { return "" }

func lastModifiedAt(current any) string {
	if def, ok := current.(*agent.Definition); ok && def != nil && !def.LoadedAt.IsZero() {
		return def.LoadedAt.UTC().Format(time.RFC3339)
	}
	return ""
}

// matchesETag accepts a comma-separated If-Match list and tolerates the weak
// validator prefix, which proxies add without asking.
func matchesETag(supplied, current string) bool {
	for _, candidate := range strings.Split(supplied, ",") {
		candidate = strings.TrimSpace(candidate)
		candidate = strings.TrimPrefix(candidate, "W/")
		if candidate == current {
			return true
		}
	}
	return false
}
