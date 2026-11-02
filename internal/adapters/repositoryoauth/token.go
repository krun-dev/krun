package repositoryoauth

import (
	"context"
	"errors"
	"net/http"
	"time"

	appconfig "github.com/krun-dev/krun/internal/config"
	"golang.org/x/oauth2"
)

type Grant struct {
	AccessToken, TokenType, RefreshToken string
	Expiry                               time.Time
}
type Unavailable struct{ Cause error }

func (e *Unavailable) Error() string { return "repository OAuth transport unavailable" }
func (e *Unavailable) Unwrap() error { return e.Cause }
func (c *Client) Exchange(ctx context.Context, provider appconfig.WorkerRepositoryProvider, code string) (Grant, error) {
	confined, cancel, err := c.RepositoryOAuthContext(ctx, provider)
	if err != nil {
		return Grant{}, &Unavailable{Cause: err}
	}
	defer cancel()
	token, err := c.RepositoryOAuthConfig(provider).Exchange(confined, code)
	if err != nil {
		return Grant{}, err
	}
	return Grant{AccessToken: token.AccessToken, TokenType: token.TokenType, RefreshToken: token.RefreshToken, Expiry: token.Expiry}, nil
}
func (c *Client) Refresh(ctx context.Context, provider appconfig.WorkerRepositoryProvider, current Grant) (Grant, error) {
	confined, cancel, err := c.RepositoryOAuthContext(ctx, provider)
	if err != nil {
		return Grant{}, &Unavailable{Cause: err}
	}
	defer cancel()
	// The lease owner must refresh even if the old access token has a brief grace period.
	token, err := c.RepositoryOAuthConfig(provider).TokenSource(confined, &oauth2.Token{AccessToken: current.AccessToken, TokenType: current.TokenType, RefreshToken: current.RefreshToken, Expiry: time.Unix(1, 0)}).Token()
	if err != nil {
		return Grant{}, err
	}
	return Grant{AccessToken: token.AccessToken, TokenType: token.TokenType, RefreshToken: token.RefreshToken, Expiry: token.Expiry}, nil
}
func GrantRejected(err error) bool {
	var response *oauth2.RetrieveError
	if !errors.As(err, &response) || response.Response == nil {
		return false
	}
	return response.Response.StatusCode == http.StatusBadRequest || response.Response.StatusCode == http.StatusUnauthorized
}
func IsUnavailable(err error) bool { var target *Unavailable; return errors.As(err, &target) }
