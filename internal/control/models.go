package control

import (
	"github.com/krun-dev/krun/internal/domain"
)

type User = domain.User

type Organization = domain.Organization

type Member = domain.Member

type Group = domain.Group

type Conditions = domain.Conditions

type Assignment = domain.Assignment

type Resource = domain.Resource

type Selector = domain.Selector

type Statement = domain.Statement

type Policy = domain.Policy

type APIKey = domain.APIKey

type Session = domain.Session

type Browser = domain.Browser

type Login = domain.Login

type Audit = domain.Audit

type State = domain.State

type WorkerAssetsConfig = domain.WorkerAssetsConfig

type WorkerActor = domain.WorkerActor

type WorkerBuild = domain.WorkerBuild

type WorkerBuildPrivate = domain.WorkerBuildPrivate

type WorkerArtifact = domain.WorkerArtifact

type WorkerEnvironment = domain.WorkerEnvironment

type Worker = domain.Worker

type WorkerPlacement = domain.WorkerPlacement

type WorkerVersion = domain.WorkerVersion

type WorkerSecretVersion = domain.WorkerSecretVersion

type WorkerStep = domain.WorkerStep

type WorkerOperation = domain.WorkerOperation

type WorkerOperationPrivate = domain.WorkerOperationPrivate

type WorkerHistory = domain.WorkerHistory

type WorkerIdempotency = domain.WorkerIdempotency

type WorkerAck = domain.WorkerAck

type WorkerState = domain.WorkerState

type WorkerRepositoryConnection = domain.WorkerRepositoryConnection

type WorkerRepositoryConnectState = domain.WorkerRepositoryConnectState

type WorkerRepositorySource = domain.WorkerRepositorySource

type WorkerBuildRepository = domain.WorkerBuildRepository

type encryptedWorkerSecret = domain.EncryptedWorkerSecret

type workerConfigRoute = domain.EdgeConfigRoute

type workerConfigProjection = domain.EdgeConfigProjection

type workerConfigOp = domain.EdgeConfigOp

type APIError = domain.APIError

var newState = domain.NewState

var memberKey = domain.MemberKey

var workerPlacementKey = domain.WorkerPlacementKey

var activeWorkerPlacement = domain.ActiveWorkerPlacement

var sameWorkerSecretVersion = domain.SameWorkerSecretVersion

var cloneState = domain.CloneState

var problem = domain.Problem

var notFound = domain.NotFound

var forbidden = domain.Forbidden
