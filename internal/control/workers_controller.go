package control

import (
	"github.com/krun-dev/krun/internal/adapters/storagecredentials"
	workerapp "github.com/krun-dev/krun/internal/workers"
)

func newWorkersController(s *Server) (*workerapp.Controller, error) {
	controller, err := workerapp.NewController(s.workerApplication(nil))
	if err != nil {
		return nil, err
	}
	controller.ReadCredentials, err = storagecredentials.NewArtifactManager(s.Config.Workers)
	if err != nil {
		return nil, err
	}
	return controller, nil
}
