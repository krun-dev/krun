package storage

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/krun-dev/krun/internal/domain"
)

var ErrDeviceCodeCollision = errors.New("device code collision")

func (s *Store) DeviceByCode(ctx context.Context, hash string) (domain.Device, bool, error) {
	if s.pool == nil {
		device, ok := s.memState().Devices[hash]
		return device, ok, nil
	}
	var out domain.Device
	err := s.pool.QueryRow(ctx, `SELECT code_hash, user_code, token, expires_at, approved, consumed FROM krun_device_codes WHERE code_hash=$1`, hash).Scan(&out.CodeHash, &out.UserCode, &out.Token, &out.Expires, &out.Approved, &out.Consumed)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Device{}, false, nil
	}
	if err != nil {
		return domain.Device{}, false, err
	}
	return out, true, nil
}

func (s *Store) DeviceByUserCode(ctx context.Context, userCode string) (domain.Device, bool, error) {
	if s.pool == nil {
		for _, device := range s.memState().Devices {
			if device.UserCode == userCode {
				return device, true, nil
			}
		}
		return domain.Device{}, false, nil
	}
	var out domain.Device
	err := s.pool.QueryRow(ctx, `SELECT code_hash, user_code, token, expires_at, approved, consumed FROM krun_device_codes WHERE user_code=$1`, userCode).Scan(&out.CodeHash, &out.UserCode, &out.Token, &out.Expires, &out.Approved, &out.Consumed)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Device{}, false, nil
	}
	if err != nil {
		return domain.Device{}, false, err
	}
	return out, true, nil
}

func (s *Store) InsertDevice(ctx context.Context, device domain.Device) error {
	if s.pool == nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.memory == nil {
			return errors.New("store is not initialized")
		}
		if _, exists := s.memory.Devices[device.CodeHash]; exists {
			return ErrDeviceCodeCollision
		}
		for _, existing := range s.memory.Devices {
			if existing.UserCode == device.UserCode {
				return ErrDeviceCodeCollision
			}
		}
		s.memory.Devices[device.CodeHash] = device
		return nil
	}
	tag, err := s.pool.Exec(ctx, `INSERT INTO krun_device_codes (code_hash, user_code, token, expires_at, approved, consumed) VALUES ($1,$2,$3,$4,$5,$6) ON CONFLICT DO NOTHING`, device.CodeHash, device.UserCode, device.Token, device.Expires, device.Approved, device.Consumed)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrDeviceCodeCollision
	}
	return err
}

// consumeDevice clears the token and marks the code used in one statement, so
// two concurrent polls cannot both collect a CLI credential.
func (s *Store) ConsumeDevice(ctx context.Context, hash string) (string, bool, error) {
	if s.pool == nil {
		st := s.memState()
		device, ok := st.Devices[hash]
		if !ok || device.Consumed || !device.Approved || device.Token == "" || !now().Before(device.Expires) {
			return "", false, nil
		}
		token := device.Token
		device.Consumed, device.Token = true, ""
		st.Devices[hash] = device
		return token, true, nil
	}
	var token string
	err := s.pool.QueryRow(ctx, `WITH candidate AS MATERIALIZED (
		SELECT code_hash, token FROM krun_device_codes
		WHERE code_hash=$1 AND consumed=false AND approved=true AND token <> '' AND expires_at > now()
		FOR UPDATE
	)
	UPDATE krun_device_codes AS device SET consumed=true, token=''
	FROM candidate WHERE device.code_hash=candidate.code_hash
	RETURNING candidate.token`, hash).Scan(&token)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return token, true, nil
}

// approveDevice conditionally approves a live device code and creates the CLI
// session in the same transaction. A losing approval therefore cannot leave
// behind an orphan credential.
func (s *Store) ApproveDevice(ctx context.Context, hash, token string, session domain.Session) (bool, error) {
	return s.ApproveDeviceAudited(ctx, hash, token, session, nil)
}

func (s *Store) ApproveDeviceAudited(ctx context.Context, hash, token string, session domain.Session, audits []domain.Audit) (bool, error) {
	if s.pool == nil {
		st := s.memState()
		if st == nil {
			return false, errors.New("store is not initialized")
		}
		device, ok := st.Devices[hash]
		if !ok || device.Approved || device.Consumed || !now().Before(device.Expires) {
			return false, nil
		}
		device.Approved, device.Token = true, token
		st.Devices[hash] = device
		st.Sessions[digest(token)] = session
		return true, nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `UPDATE krun_device_codes SET approved=true, token=$2 WHERE code_hash=$1 AND consumed=false AND approved=false AND expires_at > now()`, hash, token)
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() != 1 {
		return false, nil
	}
	if _, err = tx.Exec(ctx, `INSERT INTO krun_sessions (session_hash, user_id, csrf, auth_version, cli, created_at, seen_at, absolute_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, digest(token), session.User, session.CSRF, session.AuthVersion, session.CLI, session.Created, session.Seen, session.Absolute); err != nil {
		return false, err
	}
	if err = insertAudits(ctx, tx, audits); err != nil {
		return false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}
