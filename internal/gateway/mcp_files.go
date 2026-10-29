package gateway

import (
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gofiber/fiber/v2"

	"github.com/soulacy/soulacy/internal/mcp"
)

// maxMCPFileBytes caps an uploaded MCP auth file. Cookie jars and Playwright
// storage-state blobs are well under this; it exists to stop a runaway upload.
const maxMCPFileBytes = 2 << 20 // 2 MiB

// handleListMCPFiles returns the auth files attached to one MCP server.
//
//	GET /api/v1/mcp/:id/files
func (s *Server) handleListMCPFiles(c *fiber.Ctx) error {
	if s.credVault == nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "credential vault is not configured")
	}
	id := c.Params("id")
	if !s.mcpServerExists(id) {
		return s.errMsg(c, fiber.StatusNotFound, "unknown MCP server")
	}
	return s.listMCPFilesResponse(c, id, fiber.StatusOK)
}

// handleUploadMCPFile stores an uploaded file for an MCP server in the encrypted
// vault. It is materialized to $SOULACY_MCP_FILE_DIR for the server at launch.
//
//	POST /api/v1/mcp/:id/files   (multipart: file, optional name)
func (s *Server) handleUploadMCPFile(c *fiber.Ctx) error {
	if s.credVault == nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "credential vault is not configured")
	}
	id := c.Params("id")
	if !s.mcpServerExists(id) {
		return s.errMsg(c, fiber.StatusNotFound, "unknown MCP server")
	}
	fh, err := c.FormFile("file")
	if err != nil {
		return s.errMsg(c, fiber.StatusBadRequest, `a file upload is required (multipart field "file")`)
	}
	if fh.Size > maxMCPFileBytes {
		return s.errMsg(c, fiber.StatusRequestEntityTooLarge, fmt.Sprintf("file exceeds %d bytes", maxMCPFileBytes))
	}
	rawName := strings.TrimSpace(c.FormValue("name"))
	if rawName == "" {
		rawName = fh.Filename
	}
	name, ok := sanitizeMCPFileName(rawName)
	if !ok {
		return s.errMsg(c, fiber.StatusBadRequest, "invalid file name; use a plain file name with no path separators")
	}
	f, err := fh.Open()
	if err != nil {
		return s.errMsg(c, fiber.StatusInternalServerError, "could not read upload")
	}
	data, err := io.ReadAll(io.LimitReader(f, maxMCPFileBytes+1))
	_ = f.Close()
	if err != nil {
		return s.errMsg(c, fiber.StatusInternalServerError, "could not read upload")
	}
	if int64(len(data)) > maxMCPFileBytes {
		return s.errMsg(c, fiber.StatusRequestEntityTooLarge, fmt.Sprintf("file exceeds %d bytes", maxMCPFileBytes))
	}
	if len(data) == 0 {
		return s.errMsg(c, fiber.StatusBadRequest, "uploaded file is empty")
	}
	if err := s.credVault.WriteBlob(c.UserContext(), mcp.FileVaultNamespace(id), name, data); err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	return s.listMCPFilesResponse(c, id, fiber.StatusCreated)
}

// handleDeleteMCPFile removes one attached file.
//
//	DELETE /api/v1/mcp/:id/files/:name
func (s *Server) handleDeleteMCPFile(c *fiber.Ctx) error {
	if s.credVault == nil {
		return s.errMsg(c, fiber.StatusServiceUnavailable, "credential vault is not configured")
	}
	id := c.Params("id")
	if !s.mcpServerExists(id) {
		return s.errMsg(c, fiber.StatusNotFound, "unknown MCP server")
	}
	name, ok := sanitizeMCPFileName(c.Params("name"))
	if !ok {
		return s.errMsg(c, fiber.StatusBadRequest, "invalid file name")
	}
	if err := s.credVault.Delete(c.UserContext(), mcp.FileVaultNamespace(id), name); err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	return s.listMCPFilesResponse(c, id, fiber.StatusOK)
}

// listMCPFilesResponse renders the current attached-file list. Secret contents
// are never returned — only the name and byte size.
func (s *Server) listMCPFilesResponse(c *fiber.Ctx, id string, status int) error {
	ns := mcp.FileVaultNamespace(id)
	names, err := s.credVault.List(c.UserContext(), ns)
	if err != nil {
		return s.errJSON(c, fiber.StatusInternalServerError, err)
	}
	sort.Strings(names)
	type fileView struct {
		Name string `json:"name"`
		Size int    `json:"size"`
	}
	files := make([]fileView, 0, len(names))
	for _, n := range names {
		size := 0
		if blob, rerr := s.credVault.ReadBlob(c.UserContext(), ns, n); rerr == nil {
			size = len(blob)
		}
		files = append(files, fileView{Name: n, Size: size})
	}
	return c.Status(status).JSON(fiber.Map{"files": files, "dir_env": mcp.FileDirEnv})
}

// mcpServerExists reports whether an MCP server with this id is configured on
// disk. Files attach to a saved server, so an unknown id is rejected rather
// than left as an orphaned vault namespace.
func (s *Server) mcpServerExists(id string) bool {
	if s.cfgPath == "" || strings.TrimSpace(id) == "" {
		return false
	}
	raw, err := readRawConfig(s.cfgPath)
	if err != nil {
		return false
	}
	mcpMap, ok := raw["mcp"].(map[string]any)
	if !ok {
		return false
	}
	servers, ok := mcpMap["servers"].(map[string]any)
	if !ok {
		return false
	}
	_, ok = servers[id]
	return ok
}

// sanitizeMCPFileName accepts only a plain, path-free file name so a stored key
// can never escape the materialization directory.
func sanitizeMCPFileName(raw string) (string, bool) {
	name := strings.TrimSpace(raw)
	if name == "" || len(name) > 255 {
		return "", false
	}
	if name == "." || name == ".." {
		return "", false
	}
	if strings.ContainsAny(name, "/\\\x00") {
		return "", false
	}
	if name != filepath.Base(name) {
		return "", false
	}
	return name, true
}
