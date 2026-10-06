package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// Member is someone who has used the console, with the roles their last
// token gave them (the users cache; Keycloak is the source).
type Member struct {
	Email       string   `json:"email"`
	Name        string   `json:"name"`
	Roles       []string `json:"roles"`
	FirstSeenAt int64    `json:"firstSeenAt"` // epoch ms
	LastSeenAt  int64    `json:"lastSeenAt"`
}

// TouchUser records that email used the console now, with these roles.
func (s *Store) TouchUser(ctx context.Context, tenant, email, name string, roles []string) error {
	if roles == nil {
		roles = []string{}
	}
	_, err := s.Config.Exec(ctx, `
		INSERT INTO users (tenant_id, email, name, roles) VALUES ($1,$2,$3,$4)
		ON CONFLICT (tenant_id, email) DO UPDATE SET name = excluded.name, roles = excluded.roles, last_seen_at = now()`,
		tenant, email, name, roles)
	return err
}

// Members is everyone in the cache, most recently seen first.
func (s *Store) Members(ctx context.Context, tenant string) ([]Member, error) {
	rows, _ := s.Config.Query(ctx, `SELECT email, name, roles, first_seen_at, last_seen_at FROM users WHERE tenant_id = $1 ORDER BY last_seen_at DESC, email`, tenant)
	return collect(rows, func(r pgx.Rows) (Member, error) {
		var m Member
		var first, last time.Time
		err := r.Scan(&m.Email, &m.Name, &m.Roles, &first, &last)
		m.FirstSeenAt, m.LastSeenAt = first.UnixMilli(), last.UnixMilli()
		return m, err
	})
}

// KeyOwner is who owns key id: who may change it besides an admin.
func (s *Store) KeyOwner(ctx context.Context, tenant, id string) (string, error) {
	var owner string
	err := s.Config.QueryRow(ctx, `SELECT owner FROM api_keys WHERE tenant_id = $1 AND id = $2`, tenant, id).Scan(&owner)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return owner, err
}
