package control

import "time"

type User struct {
	ID          string `json:"user_id"`
	Source      string `json:"source_id"`
	Subject     string `json:"subject"`
	Name        string `json:"name"`
	Email       string `json:"email,omitempty"`
	Status      string `json:"status"`
	AuthVersion int    `json:"auth_version"`
}
type Organization struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Sources []string `json:"allowed_sources"`
}
type Member struct {
	Organization string `json:"organization_id"`
	User         string `json:"user_id"`
}
type Group struct {
	ID        string   `json:"id"`
	ScopeKind string   `json:"scope_kind"`
	ScopeID   string   `json:"scope_id"`
	Name      string   `json:"name"`
	Members   []string `json:"members"`
	Revision  int      `json:"revision"`
}
type Conditions struct {
	Environments []string `json:"environments,omitempty"`
}
type Assignment struct {
	ID          string     `json:"assignment_id"`
	SubjectKind string     `json:"subject_kind"`
	SubjectID   string     `json:"subject_id"`
	Role        string     `json:"role_id"`
	ScopeKind   string     `json:"scope_kind"`
	ScopeID     string     `json:"scope_id"`
	Conditions  Conditions `json:"conditions"`
	Revision    int        `json:"revision"`
}
type Resource struct {
	ID           string `json:"id"`
	Kind         string `json:"kind"`
	Organization string `json:"organization_id"`
	Name         string `json:"name"`
	Revision     int    `json:"revision"`
}
type Selector struct {
	Organization string   `json:"organization_id,omitempty"`
	Kind         string   `json:"kind"`
	IDs          []string `json:"ids,omitempty"`
	All          bool     `json:"all_in_organization,omitempty"`
	AllSystem    bool     `json:"all_in_system,omitempty"`
}
type Statement struct {
	Effect     string     `json:"effect"`
	Actions    []string   `json:"actions"`
	Resource   Selector   `json:"resource"`
	Conditions Conditions `json:"conditions"`
}
type Policy struct {
	Version    int         `json:"schema_version"`
	Statements []Statement `json:"statements"`
}
type APIKey struct {
	ID           string     `json:"id"`
	Owner        string     `json:"owner_user_id"`
	Organization string     `json:"organization_id,omitempty"`
	ScopeKind    string     `json:"scope_kind"`
	Name         string     `json:"name"`
	Prefix       string     `json:"prefix"`
	Policy       Policy     `json:"policy"`
	Expires      *time.Time `json:"expires_at"`
	Revoked      bool       `json:"revoked"`
	Revision     int        `json:"policy_revision"`
}

func (k APIKey) active() bool {
	return !k.Revoked && (k.Expires == nil || now().Before(*k.Expires))
}

// Secret hashes are stored separately from public metadata to prevent accidental disclosure.
type Session struct {
	User                    string
	Hash                    string
	CSRF                    string
	AuthVersion             int
	Created, Seen, Absolute time.Time
	CLI                     bool
}
type Browser struct {
	CSRF       string
	Generation int
	Expires    time.Time
}
type Login struct {
	Provider, Browser, Fingerprint, Verifier, Nonce, ReturnTo string
	Generation                                                int
	Expires                                                   time.Time
	Consumed                                                  bool
}
type EmailLogin struct {
	Provider, Browser, Fingerprint, Email, CodeHash, ReturnTo, IP string
	Generation, Attempts                                          int
	Sent, Expires                                                 time.Time
	Consumed                                                      bool
}

type Device struct {
	CodeHash, UserCode, Token string
	Expires                   time.Time
	Approved, Consumed        bool
}
type Audit struct {
	ID           string    `json:"id"`
	Time         time.Time `json:"time"`
	Actor        string    `json:"actor"`
	Credential   string    `json:"credential"`
	KeyID        string    `json:"key_id,omitempty"`
	Action       string    `json:"action"`
	Resource     string    `json:"resource"`
	Organization string    `json:"organization_id,omitempty"`
	Allowed      bool      `json:"allowed"`
	RequestID    string    `json:"request_id"`
	Revision     int       `json:"revision"`
}
type State struct {
	Revision      int
	Bootstrapped  bool
	Sources       map[string]string
	Users         map[string]User
	Organizations map[string]Organization
	Members       map[string]Member
	Groups        map[string]Group
	Assignments   map[string]Assignment
	Resources     map[string]Resource
	Keys          map[string]APIKey
	KeyHashes     map[string]string
	Sessions      map[string]Session
	Browsers      map[string]Browser
	Logins        map[string]Login
	EmailLogins   map[string]EmailLogin
	Devices       map[string]Device
	Invitations   map[string]time.Time
	Audits        []Audit
}

func newState() *State {
	return &State{Sources: map[string]string{}, Users: map[string]User{}, Organizations: map[string]Organization{}, Members: map[string]Member{}, Groups: map[string]Group{}, Assignments: map[string]Assignment{}, Resources: map[string]Resource{}, Keys: map[string]APIKey{}, KeyHashes: map[string]string{}, Sessions: map[string]Session{}, Browsers: map[string]Browser{}, Logins: map[string]Login{}, EmailLogins: map[string]EmailLogin{}, Devices: map[string]Device{}, Invitations: map[string]time.Time{}, Audits: []Audit{}}
}
func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
func memberKey(org, user string) string { return org + "/" + user }
