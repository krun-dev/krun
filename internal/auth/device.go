package auth

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/krun-dev/krun/internal/audit"
	"github.com/krun-dev/krun/internal/authorization"
	"github.com/krun-dev/krun/internal/domain"
	"github.com/krun-dev/krun/internal/securetoken"
	"github.com/krun-dev/krun/internal/storage"
)

func (s *Service) StartDevice(ctx context.Context) (device, user string, err error) {
	for attempt := 0; attempt < 8; attempt++ {
		device = securetoken.Random()
		// Fourteen hex characters retain 56 bits of entropy for the approval code.
		raw := strings.ToUpper(securetoken.Digest(securetoken.Random())[:14])
		user = raw[:7] + "-" + raw[7:]
		err = s.Store.InsertDevice(ctx, domain.Device{CodeHash: securetoken.Digest(device), UserCode: user, Expires: time.Now().UTC().Add(5 * time.Minute)})
		if err == nil {
			return device, user, nil
		}
		if !errors.Is(err, storage.ErrDeviceCodeCollision) {
			return "", "", err
		}
	}
	return "", "", errors.New("unable to allocate a unique device code")
}
func (s *Service) PollDevice(ctx context.Context, code string) (string, error) {
	hash := securetoken.Digest(code)
	device, ok, err := s.Store.DeviceByCode(ctx, hash)
	if err != nil {
		return "", err
	}
	if !ok || !time.Now().UTC().Before(device.Expires) || device.Consumed {
		return "", domain.Problem(400, "DEVICE_EXPIRED", "Login expired or consumed")
	}
	if !device.Approved {
		return "", domain.Problem(409, "AUTHORIZATION_PENDING", "Waiting for browser approval")
	}
	token, collected, err := s.Store.ConsumeDevice(ctx, hash)
	if err != nil {
		return "", err
	}
	if !collected {
		return "", domain.Problem(400, "DEVICE_EXPIRED", "Login expired or consumed")
	}
	return token, nil
}
func (s *Service) CheckDeviceApproval(ctx context.Context, p authorization.Principal, ip string) error {
	if p.Kind != "session" {
		return domain.Forbidden()
	}
	for _, limit := range []struct {
		scope, key string
		max        int
	}{{"cli-approve-user", p.User.ID, 10}, {"cli-approve-ip", ip, 30}} {
		exceeded, err := s.Store.RateLimitExceeded(ctx, limit.scope, limit.key, time.Minute, limit.max)
		if err != nil {
			return err
		}
		if exceeded {
			return domain.Problem(429, "RATE_LIMITED", "Try again later")
		}
	}
	return nil
}
func (s *Service) ApproveDevice(ctx context.Context, st *domain.State, p authorization.Principal, code, requestID string) error {
	device, ok, err := s.Store.DeviceByUserCode(ctx, code)
	if err != nil {
		return err
	}
	if !ok {
		return domain.NotFound()
	}
	if device.Approved || device.Consumed || !time.Now().UTC().Before(device.Expires) {
		return domain.Problem(400, "DEVICE_EXPIRED", "Approval expired")
	}
	token := "krun_cli_" + securetoken.Random()
	at := time.Now().UTC()
	session := domain.Session{User: p.User.ID, Hash: securetoken.Digest(token), AuthVersion: p.User.AuthVersion, Created: at, Seen: at, Absolute: at.Add(time.Hour), CLI: true}
	audit.Record(st, p, requestID, "auth.cli.approve", p.User.ID, true)
	var approved bool
	if s.Store.Persistent() {
		audits := append([]domain.Audit(nil), st.PendingAudits...)
		st.PendingAudits = nil
		approved, err = s.Store.ApproveDeviceAudited(ctx, device.CodeHash, token, session, audits)
	} else {
		approved, err = s.Store.ApproveDevice(ctx, device.CodeHash, token, session)
	}
	if err != nil {
		return err
	}
	if !approved {
		return domain.Problem(400, "DEVICE_EXPIRED", "Approval expired")
	}
	return nil
}
