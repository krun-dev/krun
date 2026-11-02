package domain

import (
	cedar "github.com/cedar-policy/cedar-go"
)

type AuthorizationCache struct {
	Users map[string]*cedar.PolicySet
	Keys  map[string]*cedar.PolicySet
}

func NewAuthorizationCache() *AuthorizationCache {
	return &AuthorizationCache{Users: map[string]*cedar.PolicySet{}, Keys: map[string]*cedar.PolicySet{}}
}
