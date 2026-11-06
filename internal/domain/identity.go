package domain

type ExternalIdentity struct {
	Subject, Name, Email string
	EmailVerified        bool
}
