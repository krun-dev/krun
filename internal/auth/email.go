package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"fmt"
	"math/big"
	"time"

	"github.com/krun-dev/krun/internal/audit"
	"github.com/krun-dev/krun/internal/authorization"
	appconfig "github.com/krun-dev/krun/internal/config"
	"github.com/krun-dev/krun/internal/domain"
	"github.com/krun-dev/krun/internal/securetoken"
	"github.com/krun-dev/krun/internal/storage"
)

type EmailChallengeInput struct {
	Provider                                   appconfig.ProviderConfig
	Email, ReturnTo, BrowserHash, IP, Language string
	Browser                                    domain.Browser
}

func (s *Service) IssueEmail(ctx context.Context, in EmailChallengeInput, send func(context.Context, string, string, string) error) (string, error) {
	number, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err != nil {
		return "", err
	}
	code := fmt.Sprintf("%06d", number.Int64())
	id := securetoken.Random()
	at := time.Now().UTC()
	challenge := domain.EmailLogin{Provider: in.Provider.ID, Browser: in.BrowserHash, Fingerprint: appconfig.LoginFingerprint(in.Provider), Email: in.Email, CodeHash: securetoken.Digest(id + "\x00" + code), ReturnTo: in.ReturnTo, IP: in.IP, Sent: at, Expires: at.Add(10 * time.Minute)}
	if _, err = s.Store.BeginEmailLogin(ctx, in.BrowserHash, in.Browser, securetoken.Digest(id), challenge, time.Minute, 10); err != nil {
		return "", err
	}
	if err = send(ctx, in.Email, code, in.Language); err != nil {
		// Preserve the cooldown, but invalidate a code whose message was not delivered.
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = s.Store.InvalidateEmailLogin(cleanup, securetoken.Digest(id))
		return "", domain.Problem(503, "EMAIL_DELIVERY_FAILED", "Unable to send the verification email. Try again later")
	}
	return id, nil
}

type EmailVerifyInput struct {
	Provider                                appconfig.ProviderConfig
	Challenge, Code, BrowserHash, RequestID string
	Browser                                 domain.Browser
}

func (s *Service) VerifyEmail(ctx context.Context, in EmailVerifyInput) (domain.EmailLogin, error) {
	key := securetoken.Digest(in.Challenge)
	challenge, found, err := s.Store.EmailLogin(ctx, key)
	if err != nil {
		return domain.EmailLogin{}, err
	}
	invalid := func() (domain.EmailLogin, error) {
		return domain.EmailLogin{}, domain.Problem(400, "EMAIL_CODE_INVALID", "Invalid or expired verification code")
	}
	if !found || challenge.Provider != in.Provider.ID || challenge.Browser != in.BrowserHash || challenge.Fingerprint != appconfig.LoginFingerprint(in.Provider) || challenge.Generation != in.Browser.Generation || challenge.Consumed || challenge.Attempts >= 5 || !time.Now().UTC().Before(challenge.Expires) {
		return invalid()
	}
	if len(in.Code) != 6 || subtle.ConstantTimeCompare([]byte(securetoken.Digest(in.Challenge+"\x00"+in.Code)), []byte(challenge.CodeHash)) != 1 {
		// Persist the attempt conditionally so concurrent failures cannot reset it.
		if _, err = s.Store.FailEmailLogin(ctx, key); err != nil {
			return domain.EmailLogin{}, err
		}
		s.RecordAnonymous(ctx, in.Provider.ID, in.RequestID, "auth.login.failed.EMAIL_CODE_INVALID")
		return invalid()
	}
	return challenge, nil
}
func (s *Service) RecordAnonymous(ctx context.Context, provider, requestID, action string) {
	_ = s.Store.Update(storage.WithReadScope(ctx, storage.ReadScope{ActorOnly: true}), storage.PartitionIAM, func(st *domain.State) error {
		audit.Record(st, authorization.Principal{Kind: "anonymous"}, requestID, action, provider, false)
		return nil
	})
}
