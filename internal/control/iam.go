package control

import (
	"github.com/krun-dev/krun/internal/iam"
)

func (s *Server) iam() iam.Service { return iam.NewService(s.sourceEnabled) }
