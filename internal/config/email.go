package config

import (
	"errors"
	"net"
	"net/mail"
	slices "slices"
	"strings"
)

func NormalizedEmail(value string) (string, error) {
	value = strings.TrimSpace(value)
	address, err := mail.ParseAddress(value)
	if err != nil || address.Address != value || len(value) > 254 || strings.ContainsAny(value, "\r\n") || !strings.Contains(value, "@") {
		return "", errors.New("a valid email address is required")
	}
	// Email login treats mailbox names as case-insensitive; this is the stable subject.
	return strings.ToLower(value), nil
}

func (c *Config) ValidateSMTP() error {
	s := &c.SMTP
	if s.Host == "" || strings.ContainsAny(s.Host, "/\r\n \t") {
		return errors.New("SMTP host and from address required for email login")
	}
	from, err := NormalizedEmail(s.From)
	if err != nil {
		return errors.New("SMTP host and from address required for email login")
	}
	s.From = from
	if s.TLS == "" {
		s.TLS = "starttls"
	}
	if !slices.Contains([]string{"starttls", "tls", "none"}, s.TLS) {
		return errors.New("SMTP tls must be starttls, tls or none")
	}
	if s.TLS == "none" && (!c.Development || (s.Host != "localhost" && !net.ParseIP(s.Host).IsLoopback())) {
		return errors.New("unencrypted SMTP is only allowed on loopback in development")
	}
	if s.Port == 0 {
		s.Port = 587
		if s.TLS == "tls" {
			s.Port = 465
		}
	}
	if s.Port < 1 || s.Port > 65535 {
		return errors.New("invalid SMTP port")
	}
	if (s.Username == "") != (s.Password == "") {
		return errors.New("SMTP username and password must be configured together")
	}
	return nil
}
