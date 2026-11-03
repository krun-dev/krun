package storage

import (
	"github.com/krun-dev/krun/internal/domain"
)

type WorkerPlacement = domain.WorkerPlacement
type WorkerBuild = domain.WorkerBuild
type WorkerArtifact = domain.WorkerArtifact
type WorkerVersion = domain.WorkerVersion

var newState = domain.NewState
var newWorkerState = domain.NewWorkerState
var cloneState = domain.CloneState
