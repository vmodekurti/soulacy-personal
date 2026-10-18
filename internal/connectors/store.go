package connectors

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	_ "github.com/mattn/go-sqlite3"
	"github.com/soulacy/soulacy/internal/sqlitex"
)

var ErrConnectorNotFound = errors.New("connector not found")

type Store struct{ db *sql.DB }

const connectorSchema = `
CREATE TABLE IF NOT EXISTS user_connectors (
  id TEXT NOT NULL,
  workspace_id TEXT NOT NULL,
  owner_subject TEXT NOT NULL DEFAULT '',
  name TEXT NOT NULL,
  intent TEXT NOT NULL,
  category TEXT NOT NULL,
  status TEXT NOT NULL,
  skill_name TEXT NOT NULL DEFAULT '',
  sites_json TEXT NOT NULL,
  created_at TIMESTAMP NOT NULL,
  updated_at TIMESTAMP NOT NULL,
  PRIMARY KEY (workspace_id, id)
);
CREATE INDEX IF NOT EXISTS user_connectors_updated
  ON user_connectors(workspace_id, owner_subject, updated_at DESC);
`

func OpenStore(path string) (*Store, error) {
	db, err := sqlitex.Open(path, sqlitex.DefaultOptions())
	if err != nil {
		return nil, fmt.Errorf("connectors: open: %w", err)
	}
	if _, err := db.Exec(connectorSchema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("connectors: schema: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Create(ctx context.Context, in Connector) (Connector, error) {
	if in.ID == "" {
		in.ID = "connector_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	}
	normalized, err := NormalizeConnector(in)
	if err != nil {
		return Connector{}, err
	}
	now := time.Now().UTC()
	normalized.CreatedAt = now
	normalized.UpdatedAt = now
	sites, err := json.Marshal(normalized.Sites)
	if err != nil {
		return Connector{}, err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO user_connectors
      (id,workspace_id,owner_subject,name,intent,category,status,skill_name,sites_json,created_at,updated_at)
      VALUES(?,?,?,?,?,?,?,?,?,?,?)`, normalized.ID, normalized.WorkspaceID, normalized.OwnerSubject,
		normalized.Name, normalized.Intent, normalized.Category, normalized.Status, normalized.SkillName, sites, now, now)
	if err != nil {
		return Connector{}, fmt.Errorf("connectors: create: %w", err)
	}
	return normalized, nil
}

func (s *Store) List(ctx context.Context, workspaceID, subject string) ([]Connector, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,workspace_id,owner_subject,name,intent,category,status,
      skill_name,sites_json,created_at,updated_at FROM user_connectors
      WHERE workspace_id=? AND owner_subject=? ORDER BY updated_at DESC`, workspaceID, subject)
	if err != nil {
		return nil, fmt.Errorf("connectors: list: %w", err)
	}
	defer rows.Close()
	out := []Connector{}
	for rows.Next() {
		item, err := scanConnector(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) Get(ctx context.Context, workspaceID, subject, id string) (Connector, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id,workspace_id,owner_subject,name,intent,category,status,
      skill_name,sites_json,created_at,updated_at FROM user_connectors
      WHERE workspace_id=? AND owner_subject=? AND id=?`, workspaceID, subject, id)
	item, err := scanConnector(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Connector{}, ErrConnectorNotFound
	}
	return item, err
}

func (s *Store) Update(ctx context.Context, in Connector) (Connector, error) {
	current, err := s.Get(ctx, in.WorkspaceID, in.OwnerSubject, in.ID)
	if err != nil {
		return Connector{}, err
	}
	normalized, err := NormalizeConnector(in)
	if err != nil {
		return Connector{}, err
	}
	normalized.CreatedAt = current.CreatedAt
	normalized.UpdatedAt = time.Now().UTC()
	sites, err := json.Marshal(normalized.Sites)
	if err != nil {
		return Connector{}, err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE user_connectors SET name=?,intent=?,category=?,status=?,
      skill_name=?,sites_json=?,updated_at=? WHERE workspace_id=? AND owner_subject=? AND id=?`,
		normalized.Name, normalized.Intent, normalized.Category, normalized.Status, normalized.SkillName,
		sites, normalized.UpdatedAt, normalized.WorkspaceID, normalized.OwnerSubject, normalized.ID)
	if err != nil {
		return Connector{}, fmt.Errorf("connectors: update: %w", err)
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return Connector{}, ErrConnectorNotFound
	}
	return normalized, nil
}

func (s *Store) SetSiteConnection(ctx context.Context, workspaceID, subject, connectorID, siteID, connectionID string) (Connector, error) {
	item, err := s.Get(ctx, workspaceID, subject, connectorID)
	if err != nil {
		return Connector{}, err
	}
	found := false
	for i := range item.Sites {
		if item.Sites[i].ID == siteID {
			item.Sites[i].AuthConnectionID = strings.TrimSpace(connectionID)
			found = true
			break
		}
	}
	if !found {
		return Connector{}, errors.New("connector site not found")
	}
	return s.Update(ctx, item)
}

func (s *Store) Delete(ctx context.Context, workspaceID, subject, id string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM user_connectors WHERE workspace_id=? AND owner_subject=? AND id=?`, workspaceID, subject, id)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return ErrConnectorNotFound
	}
	return nil
}

type connectorScanner interface{ Scan(...any) error }

func scanConnector(row connectorScanner) (Connector, error) {
	var item Connector
	var sites []byte
	if err := row.Scan(&item.ID, &item.WorkspaceID, &item.OwnerSubject, &item.Name, &item.Intent,
		&item.Category, &item.Status, &item.SkillName, &sites, &item.CreatedAt, &item.UpdatedAt); err != nil {
		return Connector{}, err
	}
	if err := json.Unmarshal(sites, &item.Sites); err != nil {
		return Connector{}, fmt.Errorf("connectors: decode sites: %w", err)
	}
	if item.Sites == nil {
		item.Sites = []Site{}
	}
	return item, nil
}
