package control

import (
	workerapp "github.com/krun-dev/krun/internal/workers"
)

func newWorkersController(s *Server) (*workerapp.Controller, error) {
	return workerapp.NewController(s.workerApplication(nil))
}
