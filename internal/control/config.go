package control

import (
	"time"

	appconfig "github.com/krun-dev/krun/internal/config"
	"github.com/krun-dev/krun/internal/securetoken"
)

var randomToken = securetoken.Random

var randomID = securetoken.NewID

var digest = securetoken.Digest

func now() time.Time { return time.Now().UTC() }

// Public entry points retained for the CLI and server commands.
type Config = appconfig.Config

type ProviderConfig = appconfig.ProviderConfig

type SMTPConfig = appconfig.SMTPConfig

type WorkersConfig = appconfig.WorkersConfig

var LoadConfig = appconfig.LoadConfig
