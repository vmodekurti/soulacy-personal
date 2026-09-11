package gateway

import (
	"database/sql"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v2"

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

func mobileStore(c *fiber.Ctx) (*mobilechan.Store, error) {
	store := mobilechan.DefaultStore()
	if store == nil {
		return nil, fiber.NewError(fiber.StatusServiceUnavailable, "mobile delivery channel is unavailable")
	}
	return store, nil
}

func mobileIdentity(c *fiber.Ctx) (workspaceID, userID string, err error) {
	identity, ok := requestIdentity(c)
	if !ok {
		return "", "", fiber.NewError(fiber.StatusUnauthorized, "authenticated identity is required")
	}
	return identity.WorkspaceID(), identity.Subject(), nil
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
	if req.PushToken != "" {
		decoded, decodeErr := hex.DecodeString(req.PushToken)
		if decodeErr != nil || len(decoded) != 32 {
			return s.errMsg(c, fiber.StatusBadRequest, "push_token must be a 32-byte APNs token encoded as hexadecimal")
		}
	}
	device := mobilechan.Device{ID: req.ID, Name: strings.TrimSpace(req.Name), PushToken: req.PushToken,
		PushEnvironment: req.PushEnvironment, BundleID: req.BundleID, NotificationsEnabled: req.NotificationsEnabled}
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
		if isProtectedSystemAgent(delivery.AgentID) {
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
	if errors.Is(err, sql.ErrNoRows) || (err == nil && isProtectedSystemAgent(delivery.AgentID)) {
		return s.errMsg(c, fiber.StatusNotFound, "delivery not found")
	}
	if err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	return c.JSON(delivery)
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

