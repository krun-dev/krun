package domain

import "time"

// Authorization and token records are individually persisted, never snapshots.
type OAuthAuthorization struct {
	Hash, Client, Redirect, Challenge, State, Scope, UserCode, Decision, User, CodeHash string
	AuthVersion                                                                         int
	Created, Expires                                                                    time.Time
	Consumed                                                                            bool
}
type OAuthGrant struct {
	ID, User, Client, Scope string
	AuthVersion             int
	Created, Expires        time.Time
	Revoked                 bool
}
type OAuthRefresh struct {
	Hash, Grant      string
	Created, Expires time.Time
	Consumed         bool
}
