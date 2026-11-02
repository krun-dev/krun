// Package repository manages repository configuration and connected user grants.
package repository

import (
	appconfig "github.com/krun-dev/krun/internal/config"
	"github.com/krun-dev/krun/internal/storage"
)

type Service struct {
	Config *appconfig.Config
	Store  *storage.Store
}
