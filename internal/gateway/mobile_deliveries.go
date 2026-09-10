package gateway

import (
	"database/sql"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"

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
}

const personalIOSBundleID = "dev.soulacy.ios"

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
	req.ID, req.PushToken = strings.TrimSpace(req.ID), strings.ToLower(strings.TrimSpace(req.PushToken))
	if req.ID == "" {
		return s.errMsg(c, fiber.StatusBadRequest, "device id is required")
	}
	if len(req.ID) > 128 || len(req.Name) > 200 {
		return s.errMsg(c, fiber.StatusBadRequest, "device id or name is too long")
	}
	if req.BundleID != "" && req.BundleID != personalIOSBundleID {
		return s.errMsg(c, fiber.StatusBadRequest, "bundle_id must identify the Soulacy iOS app")
	}
	if req.PushToken != "" {
		decoded, decodeErr := hex.DecodeString(req.PushToken)
		if decodeErr != nil || len(decoded) != 32 {
			return s.errMsg(c, fiber.StatusBadRequest, "push_token must be a 32-byte APNs token encoded as hexadecimal")
		}
	}
	device := mobilechan.Device{ID: req.ID, Name: strings.TrimSpace(req.Name), PushToken: req.PushToken,
		PushEnvironment: req.PushEnvironment, BundleID: personalIOSBundleID, NotificationsEnabled: req.NotificationsEnabled}
	if err := store.UpsertDevice(c.UserContext(), workspaceID, userID, device); err != nil {
		if errors.Is(err, mobilechan.ErrDeviceOwnership) {
			return s.errMsg(c, fiber.StatusForbidden, "device is registered to another user")
		}
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	return c.Status(fiber.StatusCreated).JSON(fiber.Map{"registered": true, "device_id": req.ID})
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
	deviceID, err := mobileDeviceID(c)
	if err != nil {
		return err
	}
	limit, _ := strconv.Atoi(c.Query("limit", "100"))
	deliveries, err := store.List(c.UserContext(), workspaceID, deviceID, userID, limit)
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
	return c.JSON(fiber.Map{"deliveries": visible, "unread_count": unread})
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
