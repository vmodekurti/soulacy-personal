package gateway

import (
	"database/sql"
	"encoding/hex"
	"errors"
	"github.com/soulacy/soulacy/internal/presentation"
	"strconv"
	"strings"
	"unicode"

	"github.com/gofiber/fiber/v2"

	"github.com/soulacy/soulacy/internal/auth"
	mobilechan "github.com/soulacy/soulacy/internal/channels/mobile"
)

type mobileDeviceRequest struct {
	ID                   string `json:"id"`
	Name                 string `json:"name"`
	PushToken            string `json:"push_token"`
	PushEnvironment      string `json:"push_environment"`
	BundleID             string `json:"bundle_id"`
	NotificationsEnabled bool   `json:"notifications_enabled"`
	// LiveStartToken is the ActivityKit push-to-start token, when the phone
	// allows agents on its lock screen.
	LiveStartToken string `json:"live_start_token"`
	// Platform is "ios" (default when empty) or "android". Android devices
	// register an opaque FCM token and the dev.soulacy.android package.
	Platform string `json:"platform"`
}

const (
	personalIOSBundleID     = mobilechan.BundleIOS
	personalAndroidBundleID = mobilechan.BundleAndroid
	// maxFCMTokenLength bounds an opaque FCM registration token.
	maxFCMTokenLength = 4096
)

func mobileStore(c *fiber.Ctx) (*mobilechan.Store, error) {
	store := mobilechan.DefaultStore()
	if store == nil {
		return nil, fiber.NewError(fiber.StatusServiceUnavailable, "mobile delivery channel is unavailable")
	}
	return store, nil
}

func mobileIdentity(c *fiber.Ctx) (workspaceID, userID string, err error) {
	// Personal is deliberately a single-workspace deployment. Static API-key
	// sessions do not carry claims; they are the local owner and use the same
	// stable identity as locally-issued JWT sessions.
	if claims := auth.ClaimsFromCtx(c); claims != nil {
		subject := strings.TrimSpace(claims.Subject)
		if subject == "" {
			subject = strings.TrimSpace(claims.Email)
		}
		if subject == "" {
			subject = "admin"
		}
		return "personal", subject, nil
	}
	return "personal", "admin", nil
}

func (s *Server) handleRegisterMobileDevice(c *fiber.Ctx) error {
	store, err := mobileStore(c)
	if err != nil {
		return err
	}
	workspaceID, userID, err := mobileIdentity(c)
	if err != nil {
		return err
	}
	var req mobileDeviceRequest
	if err := c.BodyParser(&req); err != nil {
		return s.errJSON(c, fiber.StatusBadRequest, err)
	}
	req.ID = strings.TrimSpace(req.ID)
	if req.ID == "" {
		return s.errMsg(c, fiber.StatusBadRequest, "device id is required")
	}
	if len(req.ID) > 128 || len(req.Name) > 200 {
		return s.errMsg(c, fiber.StatusBadRequest, "device id or name is too long")
	}
	var device mobilechan.Device
	switch strings.ToLower(strings.TrimSpace(req.Platform)) {
	case "", mobilechan.PlatformIOS:
		var msg string
		device, msg = iosDeviceFromRequest(req)
		if msg != "" {
			return s.errMsg(c, fiber.StatusBadRequest, msg)
		}
	case mobilechan.PlatformAndroid:
		var msg string
		device, msg = androidDeviceFromRequest(req)
		if msg != "" {
			return s.errMsg(c, fiber.StatusBadRequest, msg)
		}
	default:
		return s.errMsg(c, fiber.StatusBadRequest, `platform must be "ios" or "android"`)
	}
	if err := store.UpsertDevice(c.UserContext(), workspaceID, userID, device); err != nil {
		if errors.Is(err, mobilechan.ErrDeviceOwnership) {
			return s.errMsg(c, fiber.StatusForbidden, "device is registered to another user")
		}
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	return c.Status(fiber.StatusCreated).JSON(fiber.Map{"registered": true, "device_id": req.ID})
}

// iosDeviceFromRequest validates an iOS registration: a 32-byte hex APNs
// token and the Soulacy iOS bundle. It returns a client-facing message when
// the request is invalid.
func iosDeviceFromRequest(req mobileDeviceRequest) (mobilechan.Device, string) {
	req.PushToken = strings.ToLower(strings.TrimSpace(req.PushToken))
	if req.BundleID != "" && req.BundleID != personalIOSBundleID {
		return mobilechan.Device{}, "bundle_id must identify the Soulacy iOS app"
	}
	req.LiveStartToken = strings.ToLower(strings.TrimSpace(req.LiveStartToken))
	if req.LiveStartToken != "" && !validHexToken(req.LiveStartToken) {
		return mobilechan.Device{}, "live_start_token must be a 32-byte APNs token encoded as hexadecimal"
	}
	if req.PushToken != "" {
		decoded, decodeErr := hex.DecodeString(req.PushToken)
		if decodeErr != nil || len(decoded) != 32 {
			return mobilechan.Device{}, "push_token must be a 32-byte APNs token encoded as hexadecimal"
		}
	}
	return mobilechan.Device{ID: req.ID, Name: strings.TrimSpace(req.Name), PushToken: req.PushToken,
		PushEnvironment: req.PushEnvironment, BundleID: personalIOSBundleID, NotificationsEnabled: req.NotificationsEnabled,
		LiveStartToken: req.LiveStartToken, Platform: mobilechan.PlatformIOS}, ""
}

// androidDeviceFromRequest validates an Android registration. The FCM token
// is opaque and case-sensitive, so it is only bounded, never reformatted.
// APNs environments and Live Activity tokens do not apply and are dropped.
func androidDeviceFromRequest(req mobileDeviceRequest) (mobilechan.Device, string) {
	req.PushToken = strings.TrimSpace(req.PushToken)
	if req.BundleID != "" && req.BundleID != personalAndroidBundleID {
		return mobilechan.Device{}, "bundle_id must identify the Soulacy Android app"
	}
	if len(req.PushToken) > maxFCMTokenLength || strings.IndexFunc(req.PushToken, unicode.IsSpace) >= 0 {
		return mobilechan.Device{}, "push_token must be an FCM registration token of at most 4096 characters without whitespace"
	}
	return mobilechan.Device{ID: req.ID, Name: strings.TrimSpace(req.Name), PushToken: req.PushToken,
		BundleID: personalAndroidBundleID, NotificationsEnabled: req.NotificationsEnabled,
		Platform: mobilechan.PlatformAndroid}, ""
}

func (s *Server) handleDeleteMobileDevice(c *fiber.Ctx) error {
	store, err := mobileStore(c)
	if err != nil {
		return err
	}
	workspaceID, userID, err := mobileIdentity(c)
	if err != nil {
		return err
	}
	if err := store.DeleteDevice(c.UserContext(), workspaceID, userID, strings.TrimSpace(c.Params("id"))); err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	return c.SendStatus(fiber.StatusNoContent)
}

func mobileDeviceID(c *fiber.Ctx) (string, error) {
	id := strings.TrimSpace(c.Query("device_id"))
	if id == "" {
		return "", fiber.NewError(fiber.StatusBadRequest, "device_id is required")
	}
	return id, nil
}

func (s *Server) handleListMobileDeliveries(c *fiber.Ctx) error {
	store, err := mobileStore(c)
	if err != nil {
		return err
	}
	workspaceID, userID, err := mobileIdentity(c)
	if err != nil {
		return err
	}
	deviceID := strings.TrimSpace(c.Query("device_id"))
	limit, _ := strconv.Atoi(c.Query("limit", "100"))
	var deliveries []mobilechan.Delivery
	if deviceID == "" {
		// No device: the web feed. Show the owner what went to any of their
		// devices instead of refusing (#209).
		deliveries, err = store.ListForUser(c.UserContext(), workspaceID, userID, limit)
	} else {
		deliveries, err = store.List(c.UserContext(), workspaceID, deviceID, userID, limit)
	}
	if errors.Is(err, mobilechan.ErrDeviceOwnership) {
		return s.errMsg(c, fiber.StatusForbidden, "device is not registered to this user")
	}
	if err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	visible := deliveries[:0]
	unread := 0
	for _, delivery := range deliveries {
		if isInternalOnlyMobileAgent(delivery.AgentID) {
			continue
		}
		visible = append(visible, delivery)
		if delivery.ReadAt == nil {
			unread++
		}
	}
	// Each delivery carries how to show it (#199): the same typed blocks the
	// run ledger emits, so a brief pushed to the phone and the same brief in
	// the web feed look alike.
	out := make([]presentedDelivery, 0, len(visible))
	for _, d := range visible {
		out = append(out, presentedDelivery{Delivery: d, Presentation: presentOutput(d.Body)})
	}
	return c.JSON(fiber.Map{"deliveries": out, "unread_count": unread})
}

func (s *Server) handleGetMobileDelivery(c *fiber.Ctx) error {
	store, err := mobileStore(c)
	if err != nil {
		return err
	}
	workspaceID, userID, err := mobileIdentity(c)
	if err != nil {
		return err
	}
	deviceID, err := mobileDeviceID(c)
	if err != nil {
		return err
	}
	delivery, err := store.Get(c.UserContext(), workspaceID, c.Params("id"), deviceID, userID)
	if errors.Is(err, mobilechan.ErrDeviceOwnership) {
		return s.errMsg(c, fiber.StatusForbidden, "device is not registered to this user")
	}
	if errors.Is(err, sql.ErrNoRows) || (err == nil && isInternalOnlyMobileAgent(delivery.AgentID)) {
		return s.errMsg(c, fiber.StatusNotFound, "delivery not found")
	}
	if err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	return c.JSON(delivery)
}

// Genie is protected against mutation and external inbound routing, but it is
// a user-facing orchestrator and may legitimately publish results to the
// owner's phone. Only the low-level System agent is hidden from mobile.
func isInternalOnlyMobileAgent(id string) bool {
	return strings.TrimSpace(id) == "system"
}

func (s *Server) handleReadMobileDelivery(c *fiber.Ctx) error {
	store, err := mobileStore(c)
	if err != nil {
		return err
	}
	workspaceID, userID, err := mobileIdentity(c)
	if err != nil {
		return err
	}
	var req struct {
		DeviceID string `json:"device_id"`
	}
	if err := c.BodyParser(&req); err != nil {
		return s.errJSON(c, fiber.StatusBadRequest, err)
	}
	if err := store.MarkRead(c.UserContext(), workspaceID, c.Params("id"), strings.TrimSpace(req.DeviceID), userID); err != nil {
		if errors.Is(err, mobilechan.ErrDeviceOwnership) {
			return s.errMsg(c, fiber.StatusForbidden, "device is not registered to this user")
		}
		if errors.Is(err, sql.ErrNoRows) {
			return s.errMsg(c, fiber.StatusNotFound, "delivery not found")
		}
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	return c.JSON(fiber.Map{"read": true})
}

func (s *Server) handleListMobileFeedState(c *fiber.Ctx) error {
	store, err := mobileStore(c)
	if err != nil {
		return err
	}
	workspaceID, userID, err := mobileIdentity(c)
	if err != nil {
		return err
	}
	states, err := store.ListFeedState(c.UserContext(), workspaceID, userID)
	if err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	if states == nil {
		states = []mobilechan.FeedCardState{}
	}
	return c.JSON(fiber.Map{"cards": states})
}

func (s *Server) handlePutMobileFeedState(c *fiber.Ctx) error {
	store, err := mobileStore(c)
	if err != nil {
		return err
	}
	workspaceID, userID, err := mobileIdentity(c)
	if err != nil {
		return err
	}
	var state mobilechan.FeedCardState
	if err := c.BodyParser(&state); err != nil {
		return s.errJSON(c, fiber.StatusBadRequest, err)
	}
	state.CardID = strings.TrimSpace(state.CardID)
	if len(state.CardID) > 256 || (!strings.HasPrefix(state.CardID, "run:") && !strings.HasPrefix(state.CardID, "delivery:")) {
		return s.errMsg(c, fiber.StatusBadRequest, "card_id must identify a run or delivery")
	}
	if err := store.PutFeedState(c.UserContext(), workspaceID, userID, state); err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	return c.JSON(fiber.Map{"saved": true, "card": state})
}

// presentedDelivery is a delivery plus its presentation.
type presentedDelivery struct {
	mobilechan.Delivery
	Presentation *presentation.Presentation `json:"presentation,omitempty"`
}
