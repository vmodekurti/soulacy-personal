package gateway

import (
	"errors"

	"github.com/gofiber/fiber/v2"
	"github.com/soulacy/soulacy/internal/publishedfiles"
)

func (s *Server) publishedRoot(c *fiber.Ctx) (string, error) {
	// Unlike the legacy agent directory API this feature never runs on an
	// unauthenticated localhost gateway, even when explicitly configured.
	authenticated := s.cfg.Server.APIKey != ""
	if s.authEngine != nil {
		authenticated = s.authEngine.Effective()
	}
	if !authenticated {
		return "", fiber.NewError(fiber.StatusServiceUnavailable, "published files require gateway authentication")
	}
	id := c.Params("id")
	if s.loader.Get(id) == nil {
		return "", fiber.NewError(fiber.StatusNotFound, "agent not found")
	}
	for _, share := range s.cfg.Server.PublishedFiles {
		if share.AgentID == id {
			return share.Root, nil
		}
	}
	return "", fiber.NewError(fiber.StatusNotFound, "no published folder is configured for this agent")
}

func (s *Server) handlePublishedFiles(c *fiber.Ctx) error {
	c.Set(fiber.HeaderCacheControl, "no-store")
	root, err := s.publishedRoot(c)
	if err != nil {
		return err
	}
	list, err := publishedfiles.List(root, c.Query("path"))
	if err != nil {
		return publishedError(err)
	}
	return c.JSON(list)
}

func (s *Server) handlePublishedFile(c *fiber.Ctx) error {
	c.Set(fiber.HeaderCacheControl, "no-store")
	root, err := s.publishedRoot(c)
	if err != nil {
		return err
	}
	preview, err := publishedfiles.Read(root, c.Query("path"))
	if err != nil {
		return publishedError(err)
	}
	return c.JSON(preview)
}

func publishedError(err error) error {
	switch {
	case errors.Is(err, publishedfiles.ErrPath):
		return fiber.NewError(fiber.StatusBadRequest, err.Error())
	case errors.Is(err, publishedfiles.ErrNotFound):
		return fiber.NewError(fiber.StatusNotFound, err.Error())
	case errors.Is(err, publishedfiles.ErrPreview):
		return fiber.NewError(fiber.StatusUnsupportedMediaType, err.Error())
	case errors.Is(err, publishedfiles.ErrChanged):
		return fiber.NewError(fiber.StatusConflict, err.Error())
	default:
		return fiber.NewError(fiber.StatusServiceUnavailable, "published folder is unavailable")
	}
}
