package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
)

// ETag is a resource's version for If-Match (spec §6): a hash of the fields
// a write can change, so any write that changes one moves it.
func ETag(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return `"` + hex.EncodeToString(h[:8]) + `"`
}

// StaleError is a write whose If-Match doesn't name the resource's current
// version. Current is the resource as it is now, for the console's merge.
type StaleError struct{ Current any }

func (e *StaleError) Error() string { return "this changed since you loaded it" }

// checkMatch refuses a write whose If-Match isn't current's version. An
// empty If-Match skips the check; a missing resource matches nothing.
func checkMatch(ifMatch string, current any) error {
	if ifMatch != "" && ifMatch != ETag(current) {
		return &StaleError{Current: current}
	}
	return nil
}

// uniqueConflict turns a unique violation, from a concurrent write that got
// there first, into ErrConflict.
func uniqueConflict(err error) error {
	if pe := (*pgconn.PgError)(nil); errors.As(err, &pe) && pe.Code == "23505" {
		return ErrConflict
	}
	return err
}
