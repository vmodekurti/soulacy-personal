package gateway

import (
	"encoding/json"
	"time"

	"github.com/gofiber/fiber/v2"
)

func (s *Server) handleQueueNames(c *fiber.Ctx) error {
	return c.JSON(fiber.Map{
		"ok":     true,
		"queues": s.engine.QueueNames(),
	})
}

func (s *Server) handleQueueCreate(c *fiber.Ctx) error {
	var req struct {
		Queue string `json:"queue"`
	}
	if len(c.Body()) > 0 {
		if err := c.BodyParser(&req); err != nil {
			return s.errJSON(c, fiber.StatusBadRequest, err)
		}
	}
	created, err := s.engine.QueueCreate(req.Queue)
	if err != nil {
		return s.errJSON(c, fiber.StatusBadRequest, err)
	}
	if req.Queue == "" {
		req.Queue = "default"
	}
	return c.JSON(fiber.Map{"ok": true, "queue": req.Queue, "created": created})
}

func (s *Server) handleQueueList(c *fiber.Ctx) error {
	items, err := s.engine.QueueList(c.Query("queue"), c.QueryInt("limit", 25))
	if err != nil {
		return s.errJSON(c, fiber.StatusBadRequest, err)
	}
	return c.JSON(fiber.Map{"ok": true, "count": len(items), "items": items})
}

func (s *Server) handleQueuePut(c *fiber.Ctx) error {
	var req struct {
		Queue      string          `json:"queue"`
		Item       json.RawMessage `json:"item"`
		TTLSeconds int             `json:"ttl_seconds"`
	}
	if err := c.BodyParser(&req); err != nil {
		return s.errJSON(c, fiber.StatusBadRequest, err)
	}
	if len(req.Item) == 0 {
		return s.errMsg(c, fiber.StatusBadRequest, "item is required")
	}
	item, err := s.engine.QueuePut(req.Queue, req.Item, time.Duration(req.TTLSeconds)*time.Second)
	if err != nil {
		return s.errJSON(c, fiber.StatusBadRequest, err)
	}
	return c.Status(fiber.StatusCreated).JSON(fiber.Map{
		"ok":         true,
		"id":         item.ID,
		"queue":      item.Queue,
		"created_at": item.CreatedAt,
		"expires_at": item.ExpiresAt,
	})
}

func (s *Server) handleQueueTake(c *fiber.Ctx) error {
	item, ok, err := s.engine.QueueTake(c.Query("queue"))
	if err != nil {
		return s.errJSON(c, fiber.StatusBadRequest, err)
	}
	if !ok {
		return c.JSON(fiber.Map{"ok": false, "empty": true})
	}
	return c.JSON(fiber.Map{
		"ok":         true,
		"id":         item.ID,
		"queue":      item.Queue,
		"item":       json.RawMessage(item.Item),
		"created_at": item.CreatedAt,
		"expires_at": item.ExpiresAt,
	})
}

func (s *Server) handleQueueClear(c *fiber.Ctx) error {
	n, err := s.engine.QueueClear(c.Query("queue"))
	if err != nil {
		return s.errJSON(c, fiber.StatusBadRequest, err)
	}
	return c.JSON(fiber.Map{"ok": true, "cleared": n})
}
