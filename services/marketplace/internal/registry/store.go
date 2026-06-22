package registry

import (
	"database/sql"
	"encoding/json"
)

// Store abstracts persistence for installed integrations.
// Implementations must be safe for concurrent use.
type Store interface {
	// GetInstalled returns all currently installed integrations from storage.
	GetInstalled() ([]Integration, error)
	// Upsert saves an installed integration (webhook URL + config).
	Upsert(i Integration) error
	// Delete removes an integration installation record.
	Delete(id string) error
}

// MemoryStore is the in-memory fallback (current behavior).
// GetInstalled always returns nil — the registry map is the source of truth.
type MemoryStore struct{}

func (s *MemoryStore) GetInstalled() ([]Integration, error) { return nil, nil }
func (s *MemoryStore) Upsert(_ Integration) error           { return nil }
func (s *MemoryStore) Delete(_ string) error                { return nil }

// PostgresStore persists installation records to a marketplace_integrations table.
type PostgresStore struct{ db *sql.DB }

// NewPostgresStore wraps an already-opened *sql.DB.
func NewPostgresStore(db *sql.DB) *PostgresStore { return &PostgresStore{db: db} }

// GetInstalled returns every row in marketplace_integrations.
func (s *PostgresStore) GetInstalled() ([]Integration, error) {
	rows, err := s.db.Query("SELECT id, webhook_url, config FROM marketplace_integrations")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []Integration
	for rows.Next() {
		var id, webhookURL string
		var configJSON []byte
		if err := rows.Scan(&id, &webhookURL, &configJSON); err != nil {
			continue
		}
		var cfg map[string]string
		_ = json.Unmarshal(configJSON, &cfg)
		result = append(result, Integration{
			ID:         id,
			WebhookURL: webhookURL,
			Config:     cfg,
			Status:     StatusInstalled,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

// Upsert inserts or updates the installation record for an integration.
func (s *PostgresStore) Upsert(i Integration) error {
	configJSON, _ := json.Marshal(i.Config)
	_, err := s.db.Exec(
		`INSERT INTO marketplace_integrations (id, webhook_url, config)
		 VALUES ($1, $2, $3)
		 ON CONFLICT (id) DO UPDATE
		   SET webhook_url = EXCLUDED.webhook_url,
		       config      = EXCLUDED.config,
		       updated_at  = now()`,
		i.ID, i.WebhookURL, configJSON,
	)
	return err
}

// Delete removes an integration installation record by ID.
func (s *PostgresStore) Delete(id string) error {
	_, err := s.db.Exec("DELETE FROM marketplace_integrations WHERE id = $1", id)
	return err
}
