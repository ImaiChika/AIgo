package postgres

import (
	"aigo/internal/storage"
	"context"
	"database/sql"
	"encoding/json"
)

type authoringTxKey struct{}
type generationAccessKey struct{}
type generationAccess struct {
	UserID, Role string
	Permissions  []string
}

func (s *Store) WithAuthoringAccess(ctx context.Context, uid, role string, fn func(context.Context, []string) error) error {
	if access, ok := ctx.Value(generationAccessKey{}).(generationAccess); ok {
		if access.UserID != uid || access.Role != role {
			return storage.ErrAuthoringDenied
		}
		return fn(ctx, append([]string(nil), access.Permissions...))
	}
	// Authorization scopes can enter a short, independent quota transaction.
	// Queue before borrowing connections so a full set of outer scopes cannot
	// consume every connection needed to finish those nested operations.
	s.ensureAuthoringAdmission()
	release, err := s.authoringLimiter.Acquire(ctx)
	if err != nil {
		return err
	}
	defer release()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var defaultRole string
	var mountedJSON, directJSON []byte
	var enabled bool
	err = tx.QueryRowContext(ctx, `SELECT role,to_json(roles),to_json(permissions),enabled FROM users WHERE id=$1 FOR UPDATE`, uid).Scan(&defaultRole, &mountedJSON, &directJSON, &enabled)
	if err == sql.ErrNoRows {
		return storage.ErrAuthoringDenied
	}
	if err != nil {
		return err
	}
	if !enabled {
		return storage.ErrAuthoringDenied
	}
	var mounted, direct []string
	if err = json.Unmarshal(mountedJSON, &mounted); err != nil {
		return err
	}
	if err = json.Unmarshal(directJSON, &direct); err != nil {
		return err
	}
	if defaultRole != "" {
		mounted = append(mounted, defaultRole)
	}
	assigned := false
	hasRole := false
	for _, id := range mounted {
		if id != "" {
			hasRole = true
		}
		if id == role && id != "" {
			assigned = true
		}
	}
	if (role == "" && hasRole) || (role != "" && !assigned) {
		return storage.ErrAuthoringDenied
	}
	permissions := append([]string(nil), direct...)
	if role != "" {
		var raw []byte
		err = tx.QueryRowContext(ctx, `SELECT to_json(permissions) FROM roles WHERE id=$1 FOR SHARE`, role).Scan(&raw)
		if err == sql.ErrNoRows {
			return storage.ErrAuthoringDenied
		}
		if err != nil {
			return err
		}
		var inherited []string
		if err = json.Unmarshal(raw, &inherited); err != nil {
			return err
		}
		permissions = append(permissions, inherited...)
	}
	accessCtx := context.WithValue(ctx, authoringTxKey{}, tx)
	accessCtx = context.WithValue(accessCtx, generationAccessKey{}, generationAccess{uid, role, permissions})
	if err = fn(accessCtx, permissions); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ensureAuthoringAdmission() {
	s.authoringOnce.Do(func() {
		open := s.db.Stats().MaxOpenConnections
		if open == 0 {
			open = 10
		}
		s.authoringLimiter = storage.NewConcurrencyLimiter(min(32, max(1, open/2)))
	})
}

// AuthoringAdmissionSnapshot reports bounded process-local admission, not a
// distributed limit or a change to any model/first-check worker count.
func (s *Store) AuthoringAdmissionSnapshot() storage.ConcurrencySnapshot {
	s.ensureAuthoringAdmission()
	return s.authoringLimiter.Snapshot()
}
