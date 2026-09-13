package gateway

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/soulacy/soulacy/internal/channels/mobile"
	"github.com/soulacy/soulacy/internal/rbac"
)

func (s *Server) registerMobileNodeRoutes(api fiber.Router) {
	read := s.rbacMW(rbac.ResourceChat, rbac.ActionRead)
	write := s.rbacMW(rbac.ResourceChat, rbac.ActionChat)
	api.Get("/mobile/nodes", read, s.handleListMobileNodes)
	api.Post("/mobile/nodes/register", write, s.handleRegisterMobileNode)
	api.Get("/mobile/nodes/:device/commands", read, s.handleMobileNodeCommands)
	api.Post("/mobile/nodes/:device/commands", write, s.handleCreateMobileNodeCommand)
	api.Get("/mobile/nodes/:device/commands/:id", read, s.handleGetMobileNodeCommand)
	api.Post("/mobile/nodes/:device/commands/:id/claim", write, s.handleClaimMobileNodeCommand)
	api.Post("/mobile/nodes/:device/commands/:id/result", write, s.handleFinishMobileNodeCommand)
}

func (s *Server) mobileNodeError(c *fiber.Ctx, err error) error {
	switch {
	case errors.Is(err, mobile.ErrDeviceOwnership), errors.Is(err, sql.ErrNoRows):
		return s.errMsg(c, 404, "device or command not found")
	case errors.Is(err, mobile.ErrNodeConflict):
		return s.errMsg(c, 409, err.Error())
	case errors.Is(err, mobile.ErrNodeInvalid):
		return s.errMsg(c, 400, err.Error())
	default:
		return s.errMsg(c, 500, "mobile command storage failed")
	}
}

func (s *Server) handleRegisterMobileNode(c *fiber.Ctx) error {
	store, err := mobileStore(c)
	if err != nil {
		return err
	}
	workspace, user, err := mobileIdentity(c)
	if err != nil {
		return err
	}
	var n mobile.Node
	if c.BodyParser(&n) != nil {
		return s.errMsg(c, 400, "invalid node registration")
	}
	if err := store.RegisterNode(c.UserContext(), workspace, user, n); err != nil {
		return s.mobileNodeError(c, err)
	}
	return c.Status(201).JSON(fiber.Map{"registered": true, "device_id": n.DeviceID, "poll_interval_seconds": 5, "foreground_only": true})
}

func (s *Server) handleListMobileNodes(c *fiber.Ctx) error {
	store, err := mobileStore(c)
	if err != nil {
		return err
	}
	workspace, user, err := mobileIdentity(c)
	if err != nil {
		return err
	}
	nodes, err := store.Nodes(c.UserContext(), workspace, user)
	if err != nil {
		return s.mobileNodeError(c, err)
	}
	return c.JSON(fiber.Map{"nodes": nodes, "supported_commands": mobile.SupportedNodeCommands})
}

func (s *Server) handleMobileNodeCommands(c *fiber.Ctx) error {
	store, err := mobileStore(c)
	if err != nil {
		return err
	}
	workspace, user, err := mobileIdentity(c)
	if err != nil {
		return err
	}
	commands, err := store.PendingNodeCommands(c.UserContext(), workspace, user, c.Params("device"))
	if err != nil {
		return s.mobileNodeError(c, err)
	}
	return c.JSON(fiber.Map{"commands": commands})
}

func (s *Server) handleCreateMobileNodeCommand(c *fiber.Ctx) error {
	store, err := mobileStore(c)
	if err != nil {
		return err
	}
	workspace, user, err := mobileIdentity(c)
	if err != nil {
		return err
	}
	var command mobile.NodeCommand
	if c.BodyParser(&command) != nil {
		return s.errMsg(c, 400, "invalid command")
	}
	command.DeviceID = c.Params("device")
	if key := strings.TrimSpace(c.Get("Idempotency-Key")); key != "" {
		command.ID = key
	}
	created, err := store.EnqueueNodeCommand(c.UserContext(), workspace, user, command)
	if err != nil {
		return s.mobileNodeError(c, err)
	}
	return c.Status(201).JSON(created)
}

func (s *Server) handleGetMobileNodeCommand(c *fiber.Ctx) error {
	store, err := mobileStore(c)
	if err != nil {
		return err
	}
	workspace, user, err := mobileIdentity(c)
	if err != nil {
		return err
	}
	command, err := store.NodeCommand(c.UserContext(), workspace, user, c.Params("device"), c.Params("id"))
	if err != nil {
		return s.mobileNodeError(c, err)
	}
	return c.JSON(command)
}

func (s *Server) handleClaimMobileNodeCommand(c *fiber.Ctx) error {
	store, err := mobileStore(c)
	if err != nil {
		return err
	}
	workspace, user, err := mobileIdentity(c)
	if err != nil {
		return err
	}
	command, err := store.ClaimNodeCommand(c.UserContext(), workspace, user, c.Params("device"), c.Params("id"))
	if err != nil {
		return s.mobileNodeError(c, err)
	}
	return c.JSON(command)
}

func (s *Server) handleFinishMobileNodeCommand(c *fiber.Ctx) error {
	store, err := mobileStore(c)
	if err != nil {
		return err
	}
	workspace, user, err := mobileIdentity(c)
	if err != nil {
		return err
	}
	var body struct {
		Status string          `json:"status"`
		Result json.RawMessage `json:"result"`
		Error  string          `json:"error"`
	}
	if c.BodyParser(&body) != nil {
		return s.errMsg(c, 400, "invalid command result")
	}
	command, err := store.FinishNodeCommand(c.UserContext(), workspace, user, c.Params("device"), c.Params("id"), body.Status, body.Result, body.Error)
	if err != nil {
		return s.mobileNodeError(c, err)
	}
	return c.JSON(command)
}
