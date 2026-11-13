package domain

import (
	"errors"
	"maps"
	"slices"
	"time"
)

// CloneState returns an independent pre-image for the storage diff writer.
// Request-local pending commands are deliberately not copied: they are
// consumed by the same transaction and were never part of the JSON snapshot.
func CloneState(src *State) (*State, error) {
	if src == nil {
		return nil, errors.New("state is not initialized")
	}
	dst := NewState()
	dst.Revision = src.Revision
	dst.Bootstrapped = src.Bootstrapped
	dst.Sources = cloneStringMap(src.Sources)
	dst.Users = maps.Clone(src.Users)
	dst.Organizations = cloneOrganizations(src.Organizations)
	dst.Members = maps.Clone(src.Members)
	dst.Groups = cloneGroups(src.Groups)
	dst.Assignments = cloneAssignments(src.Assignments)
	dst.Resources = maps.Clone(src.Resources)
	dst.Keys = cloneAPIKeys(src.Keys)
	dst.KeyHashes = cloneStringMap(src.KeyHashes)
	dst.Invitations = maps.Clone(src.Invitations)
	dst.Workers = cloneWorkerState(src.Workers)
	return dst, nil
}

func cloneStringMap(source map[string]string) map[string]string {
	if source == nil {
		return nil
	}
	out := make(map[string]string, len(source))
	for key, value := range source {
		out[key] = value
	}
	return out
}

func cloneStringMapNonNil(source map[string]string) map[string]string {
	if source == nil {
		return map[string]string{}
	}
	return cloneStringMap(source)
}

func cloneR2Bindings(source map[string]WorkerR2Binding) map[string]WorkerR2Binding {
	if source == nil {
		return nil
	}
	out := make(map[string]WorkerR2Binding, len(source))
	for name, binding := range source {
		out[name] = binding
	}
	return out
}

func cloneTimePtr(source *time.Time) *time.Time {
	if source == nil {
		return nil
	}
	value := *source
	return &value
}

func cloneOrganizations(source map[string]Organization) map[string]Organization {
	if source == nil {
		return nil
	}
	out := make(map[string]Organization, len(source))
	for id, organization := range source {
		organization.Sources = slices.Clone(organization.Sources)
		out[id] = organization
	}
	return out
}

func cloneGroups(source map[string]Group) map[string]Group {
	if source == nil {
		return nil
	}
	out := make(map[string]Group, len(source))
	for id, group := range source {
		group.Members = slices.Clone(group.Members)
		out[id] = group
	}
	return out
}

func cloneAssignments(source map[string]Assignment) map[string]Assignment {
	if source == nil {
		return nil
	}
	out := make(map[string]Assignment, len(source))
	for id, assignment := range source {
		assignment.Conditions.Environments = slices.Clone(assignment.Conditions.Environments)
		out[id] = assignment
	}
	return out
}

func cloneAPIKeys(source map[string]APIKey) map[string]APIKey {
	if source == nil {
		return nil
	}
	out := make(map[string]APIKey, len(source))
	for id, key := range source {
		key.Policy = clonePolicy(key.Policy)
		key.Expires = cloneTimePtr(key.Expires)
		out[id] = key
	}
	return out
}

func clonePolicy(source Policy) Policy {
	out := source
	out.Statements = make([]Statement, len(source.Statements))
	for index, statement := range source.Statements {
		statement.Actions = slices.Clone(statement.Actions)
		statement.Resource.IDs = slices.Clone(statement.Resource.IDs)
		statement.Conditions.Environments = slices.Clone(statement.Conditions.Environments)
		out.Statements[index] = statement
	}
	return out
}

func cloneWorkerState(source *WorkerState) *WorkerState {
	if source == nil {
		return nil
	}
	dst := &WorkerState{Revision: source.Revision}
	dst.Configurations = cloneConfigurations(source.Configurations)
	dst.Buckets = maps.Clone(source.Buckets)
	dst.Workers = cloneWorkers(source.Workers)
	dst.Placements = maps.Clone(source.Placements)
	dst.Builds = cloneBuilds(source.Builds)
	dst.BuildPrivate = cloneBuildPrivate(source.BuildPrivate)
	dst.Artifacts = cloneArtifacts(source.Artifacts)
	dst.Deployments = cloneDeployments(source.Deployments)
	dst.Versions = cloneVersions(source.Versions)
	dst.Operations = cloneOperations(source.Operations)
	dst.OperationPrivate = maps.Clone(source.OperationPrivate)
	dst.Histories = cloneHistories(source.Histories)
	dst.Idempotency = maps.Clone(source.Idempotency)
	dst.SecretVersions = maps.Clone(source.SecretVersions)
	return dst
}

func cloneConfigurations(source map[string]WorkerConfiguration) map[string]WorkerConfiguration {
	if source == nil {
		return nil
	}
	out := make(map[string]WorkerConfiguration, len(source))
	for id, configuration := range source {
		configuration.Variables = CloneWorkerVariables(configuration.Variables)
		configuration.HyperdriveBindings = cloneStringMap(configuration.HyperdriveBindings)
		configuration.R2Bindings = cloneR2Bindings(configuration.R2Bindings)
		configuration.SecretNames = slices.Clone(configuration.SecretNames)
		configuration.SecretVersions = CloneStringsMap(configuration.SecretVersions)
		out[id] = configuration
	}
	return out
}

func cloneWorkers(source map[string]Worker) map[string]Worker {
	if source == nil {
		return nil
	}
	out := make(map[string]Worker, len(source))
	for id, worker := range source {
		out[id] = cloneWorker(worker)
	}
	return out
}

func cloneWorker(source Worker) Worker {
	source.PreviewBase = cloneEnvironmentPtr(source.PreviewBase)
	if source.Repository != nil {
		repository := *source.Repository
		source.Repository = &repository
	}
	source.Domains = slices.Clone(source.Domains)
	source.Environments = cloneEnvironments(source.Environments)
	return source
}

func cloneEnvironmentPtr(source *WorkerEnvironment) *WorkerEnvironment {
	if source == nil {
		return nil
	}
	environment := cloneEnvironment(*source)
	return &environment
}

func cloneEnvironments(source map[string]WorkerEnvironment) map[string]WorkerEnvironment {
	if source == nil {
		return nil
	}
	out := make(map[string]WorkerEnvironment, len(source))
	for name, environment := range source {
		out[name] = cloneEnvironment(environment)
	}
	return out
}

func cloneEnvironment(source WorkerEnvironment) WorkerEnvironment {
	if source.AssetRetentionSeconds != nil {
		seconds := *source.AssetRetentionSeconds
		source.AssetRetentionSeconds = &seconds
	}
	source.HyperdriveBindings = cloneStringMap(source.HyperdriveBindings)
	source.ServiceBindings = maps.Clone(source.ServiceBindings)
	source.R2Bindings = cloneR2Bindings(source.R2Bindings)
	source.Domains = slices.Clone(source.Domains)
	source.Hosts = slices.Clone(source.Hosts)
	source.DisplayHosts = cloneStringMap(source.DisplayHosts)
	source.Variables = CloneWorkerVariables(source.Variables)
	source.SecretNames = slices.Clone(source.SecretNames)
	source.SecretVersions = CloneStringsMap(source.SecretVersions)
	return source
}

func cloneBuilds(source map[string]WorkerBuild) map[string]WorkerBuild {
	if source == nil {
		return nil
	}
	out := make(map[string]WorkerBuild, len(source))
	for id, build := range source {
		if build.Repository != nil {
			repository := *build.Repository
			build.Repository = &repository
		}
		if build.Assets != nil {
			assets := *build.Assets
			assets.RunWorkerFirst = slices.Clone(assets.RunWorkerFirst)
			build.Assets = &assets
		}
		build.CompatibilityFlags = slices.Clone(build.CompatibilityFlags)
		build.Finished = cloneTimePtr(build.Finished)
		build.ClaimedAt = cloneTimePtr(build.ClaimedAt)
		out[id] = build
	}
	return out
}

func cloneBuildPrivate(source map[string]WorkerBuildPrivate) map[string]WorkerBuildPrivate {
	if source == nil {
		return nil
	}
	out := make(map[string]WorkerBuildPrivate, len(source))
	for id, private := range source {
		private.Files = cloneStringMap(private.Files)
		out[id] = private
	}
	return out
}

func cloneArtifacts(source map[string]WorkerArtifact) map[string]WorkerArtifact {
	if source == nil {
		return nil
	}
	out := make(map[string]WorkerArtifact, len(source))
	for id, artifact := range source {
		artifact.CompatibilityFlags = slices.Clone(artifact.CompatibilityFlags)
		out[id] = artifact
	}
	return out
}

func cloneDeployments(source map[string]WorkerDeployment) map[string]WorkerDeployment {
	if source == nil {
		return nil
	}
	out := make(map[string]WorkerDeployment, len(source))
	for id, deployment := range source {
		deployment.Committed = cloneTimePtr(deployment.Committed)
		out[id] = deployment
	}
	return out
}

func cloneVersions(source map[string]WorkerVersion) map[string]WorkerVersion {
	if source == nil {
		return nil
	}
	out := make(map[string]WorkerVersion, len(source))
	for id, version := range source {
		version.SecretNames = slices.Clone(version.SecretNames)
		version.HyperdriveBindings = cloneStringMap(version.HyperdriveBindings)
		version.ServiceBindings = maps.Clone(version.ServiceBindings)
		version.R2Bindings = cloneR2Bindings(version.R2Bindings)
		version.CompatibilityFlags = slices.Clone(version.CompatibilityFlags)
		version.Variables = CloneWorkerVariables(version.Variables)
		version.SecretVersions = CloneStringsMap(version.SecretVersions)
		out[id] = version
	}
	return out
}

func cloneOperations(source map[string]WorkerOperation) map[string]WorkerOperation {
	if source == nil {
		return nil
	}
	out := make(map[string]WorkerOperation, len(source))
	for id, operation := range source {
		operation.Steps = slices.Clone(operation.Steps)
		operation.Finished = cloneTimePtr(operation.Finished)
		out[id] = operation
	}
	return out
}

func cloneHistories(source map[string][]WorkerHistory) map[string][]WorkerHistory {
	if source == nil {
		return nil
	}
	out := make(map[string][]WorkerHistory, len(source))
	for id, history := range source {
		out[id] = slices.Clone(history)
	}
	return out
}
