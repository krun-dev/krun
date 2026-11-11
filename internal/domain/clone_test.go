package domain

import (
	"reflect"
	"testing"
	"time"
)

func TestCloneStateCopiesReferenceFieldsAndInternalMetadata(t *testing.T) {
	now := time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)
	expires := now.Add(time.Hour)
	src := NewState()
	src.Revision = 7
	src.Bootstrapped = true
	src.Sources["source"] = "value"
	src.Users["user"] = User{ID: "user"}
	src.Organizations["org"] = Organization{ID: "org", Sources: []string{"source"}}
	src.Members["org/user"] = Member{Organization: "org", User: "user"}
	src.Groups["group"] = Group{ID: "group", Members: []string{"user"}}
	src.Assignments["assignment"] = Assignment{ID: "assignment", Conditions: Conditions{Environments: []string{"production"}}}
	src.Resources["worker"] = Resource{ID: "worker", Created: now}
	src.Keys["key"] = APIKey{
		ID:      "key",
		Expires: &expires,
		Policy: Policy{Statements: []Statement{{
			Actions:    []string{"worker.read"},
			Resource:   Selector{IDs: []string{"worker"}},
			Conditions: Conditions{Environments: []string{"production"}},
		}}},
	}
	src.KeyHashes["key"] = "hash"
	src.Invitations["source/subject"] = now

	src.Workers.Revision = 9
	src.Workers.Configurations["configuration"] = WorkerConfiguration{
		Variables:          WorkerVariables{"nested": map[string]any{"value": "original"}},
		HyperdriveBindings: map[string]string{"DB": "hyperdrive"},
		R2Bindings:         map[string]WorkerR2Binding{"BUCKET": {BucketID: "bucket"}},
		SecretNames:        []string{"SECRET"},
		SecretVersions:     map[string]string{"SECRET": "version"},
	}
	src.Workers.Buckets["bucket"] = WorkerBucket{ID: "bucket", Store: "store"}
	src.Workers.Workers["worker"] = Worker{
		PreviewBase: &WorkerEnvironment{
			Variables:      WorkerVariables{"preview": "original"},
			SecretVersions: map[string]string{"SECRET": "version"},
		},
		Repository: &WorkerRepositorySource{ProviderID: "provider"},
		Domains:    []string{"domain"},
		Environments: map[string]WorkerEnvironment{
			"production": {
				HyperdriveBindings: map[string]string{"DB": "hyperdrive"},
				R2Bindings:         map[string]WorkerR2Binding{"BUCKET": {BucketID: "bucket"}},
				Domains:            []string{"domain"},
				Hosts:              []string{"host"},
				DisplayHosts:       map[string]string{"domain": "display"},
				Variables:          WorkerVariables{"nested": []any{map[string]any{"value": "original"}}},
				SecretNames:        []string{"SECRET"},
				SecretVersions:     map[string]string{"SECRET": "version"},
			},
		},
	}
	src.Workers.Builds["build"] = WorkerBuild{
		Repository:         &WorkerBuildRepository{WorkerRepositorySource: WorkerRepositorySource{ProviderID: "provider"}},
		Assets:             &WorkerAssetsConfig{RunWorkerFirst: []string{"/index.html"}},
		CompatibilityFlags: []string{"flag"},
		Finished:           &now,
		ClaimedBy:          "pod",
		ClaimedAt:          &now,
	}
	src.Workers.BuildPrivate["build"] = WorkerBuildPrivate{Files: map[string]string{"index.js": "source"}}
	src.Workers.Artifacts["artifact"] = WorkerArtifact{CompatibilityFlags: []string{"flag"}, UseRevision: 42}
	src.Workers.Deployments["deployment"] = WorkerDeployment{Committed: &now}
	src.Workers.Versions["version"] = WorkerVersion{
		SecretNames:        []string{"SECRET"},
		HyperdriveBindings: map[string]string{"DB": "hyperdrive"},
		R2Bindings:         map[string]WorkerR2Binding{"BUCKET": {BucketID: "bucket"}},
		CompatibilityFlags: []string{"flag"},
		Variables:          WorkerVariables{"nested": map[string]any{"value": "original"}},
		SecretVersions:     map[string]string{"SECRET": "version"},
	}
	src.Workers.Operations["operation"] = WorkerOperation{Steps: []WorkerStep{{Name: "step"}}, Finished: &now}
	src.Workers.OperationPrivate["operation"] = WorkerOperationPrivate{Actor: WorkerActor{User: "user"}}
	src.Workers.Histories["worker/production"] = []WorkerHistory{{Revision: 1}}
	src.Workers.Idempotency["idempotency"] = WorkerIdempotency{Digest: "digest", ID: "id"}
	src.Workers.SecretVersions["secret"] = WorkerSecretVersion{ID: "secret"}

	src.PendingAudits = []Audit{{ID: "pending-audit"}}
	src.PendingLogin = &LoginCommit{BrowserHash: "pending-login"}
	src.PendingWorkerSecrets = map[string]EncryptedWorkerSecret{"pending": {}}
	src.PendingWorkerConfigurationGuards = map[string]int{"pending": 1}

	dst, err := CloneState(src)
	if err != nil {
		t.Fatal(err)
	}
	expected := *src
	expected.PendingAudits = nil
	expected.PendingLogin = nil
	expected.PendingWorkerSecrets = nil
	expected.PendingWorkerConfigurationGuards = nil
	if !reflect.DeepEqual(&expected, dst) {
		t.Fatal("clone changed state values")
	}
	if dst.PendingAudits != nil || dst.PendingLogin != nil || dst.PendingWorkerSecrets != nil || dst.PendingWorkerConfigurationGuards != nil {
		t.Fatal("clone copied request-local pending commands")
	}

	dst.Organizations["org"] = changedOrganization(dst.Organizations["org"])
	if src.Organizations["org"].Sources[0] != "source" {
		t.Fatal("organization sources share a backing array")
	}
	dst.Groups["group"] = changedGroup(dst.Groups["group"])
	if src.Groups["group"].Members[0] != "user" {
		t.Fatal("group members share a backing array")
	}
	dst.Assignments["assignment"] = changedAssignment(dst.Assignments["assignment"])
	if src.Assignments["assignment"].Conditions.Environments[0] != "production" {
		t.Fatal("assignment conditions share a backing array")
	}
	key := dst.Keys["key"]
	key.Policy.Statements[0].Actions[0] = "changed"
	key.Policy.Statements[0].Resource.IDs[0] = "changed"
	key.Policy.Statements[0].Conditions.Environments[0] = "changed"
	*key.Expires = now
	dst.Keys["key"] = key
	if !reflect.DeepEqual(src.Keys["key"].Policy.Statements[0].Actions, []string{"worker.read"}) ||
		src.Keys["key"].Policy.Statements[0].Resource.IDs[0] != "worker" ||
		!src.Keys["key"].Expires.Equal(expires) {
		t.Fatal("API key policy or expiry shares storage with the source")
	}

	worker := dst.Workers.Workers["worker"]
	worker.Domains[0] = "changed"
	environment := worker.Environments["production"]
	environment.Hosts[0] = "changed"
	environment.DisplayHosts["domain"] = "changed"
	environment.HyperdriveBindings["DB"] = "changed"
	environment.R2Bindings["BUCKET"] = WorkerR2Binding{BucketID: "changed"}
	environment.Variables["nested"].([]any)[0].(map[string]any)["value"] = "changed"
	environment.SecretNames[0] = "changed"
	environment.SecretVersions["SECRET"] = "changed"
	worker.Environments["production"] = environment
	worker.PreviewBase.Variables["preview"] = "changed"
	worker.PreviewBase.SecretVersions["SECRET"] = "changed"
	dst.Workers.Workers["worker"] = worker
	sourceEnvironment := src.Workers.Workers["worker"].Environments["production"]
	if src.Workers.Workers["worker"].Domains[0] != "domain" ||
		sourceEnvironment.Hosts[0] != "host" ||
		sourceEnvironment.DisplayHosts["domain"] != "display" ||
		sourceEnvironment.HyperdriveBindings["DB"] != "hyperdrive" ||
		sourceEnvironment.R2Bindings["BUCKET"].BucketID != "bucket" ||
		sourceEnvironment.Variables["nested"].([]any)[0].(map[string]any)["value"] != "original" ||
		sourceEnvironment.SecretNames[0] != "SECRET" ||
		sourceEnvironment.SecretVersions["SECRET"] != "version" ||
		src.Workers.Workers["worker"].PreviewBase.Variables["preview"] != "original" {
		t.Fatal("worker environment references share storage with the source")
	}

	build := dst.Workers.Builds["build"]
	build.Repository.ProviderID = "changed"
	build.Assets.RunWorkerFirst[0] = "changed"
	build.CompatibilityFlags[0] = "changed"
	*build.Finished = now
	*build.ClaimedAt = now
	dst.Workers.Builds["build"] = build
	sourceBuild := src.Workers.Builds["build"]
	if sourceBuild.Repository.ProviderID != "provider" ||
		sourceBuild.Assets.RunWorkerFirst[0] != "/index.html" ||
		sourceBuild.CompatibilityFlags[0] != "flag" ||
		!sourceBuild.Finished.Equal(now) ||
		!sourceBuild.ClaimedAt.Equal(now) {
		t.Fatal("build references share storage with the source")
	}
	dst.Workers.BuildPrivate["build"] = changedBuildPrivate(dst.Workers.BuildPrivate["build"])
	if src.Workers.BuildPrivate["build"].Files["index.js"] != "source" {
		t.Fatal("build private files share storage with the source")
	}
	artifact := dst.Workers.Artifacts["artifact"]
	artifact.CompatibilityFlags[0] = "changed"
	dst.Workers.Artifacts["artifact"] = artifact
	if src.Workers.Artifacts["artifact"].CompatibilityFlags[0] != "flag" ||
		dst.Workers.Artifacts["artifact"].UseRevision != 42 {
		t.Fatal("artifact flags or use_revision were not cloned")
	}
	deployment := dst.Workers.Deployments["deployment"]
	*deployment.Committed = now
	dst.Workers.Deployments["deployment"] = deployment
	if !src.Workers.Deployments["deployment"].Committed.Equal(now) {
		t.Fatal("deployment timestamp shares storage with the source")
	}
	version := dst.Workers.Versions["version"]
	version.SecretNames[0] = "changed"
	version.HyperdriveBindings["DB"] = "changed"
	version.R2Bindings["BUCKET"] = WorkerR2Binding{BucketID: "changed"}
	version.CompatibilityFlags[0] = "changed"
	version.Variables["nested"].(map[string]any)["value"] = "changed"
	version.SecretVersions["SECRET"] = "changed"
	dst.Workers.Versions["version"] = version
	sourceVersion := src.Workers.Versions["version"]
	if sourceVersion.SecretNames[0] != "SECRET" ||
		sourceVersion.HyperdriveBindings["DB"] != "hyperdrive" ||
		sourceVersion.R2Bindings["BUCKET"].BucketID != "bucket" ||
		sourceVersion.CompatibilityFlags[0] != "flag" ||
		sourceVersion.Variables["nested"].(map[string]any)["value"] != "original" ||
		sourceVersion.SecretVersions["SECRET"] != "version" {
		t.Fatal("version references share storage with the source")
	}
	operation := dst.Workers.Operations["operation"]
	operation.Steps[0].Name = "changed"
	*operation.Finished = now
	dst.Workers.Operations["operation"] = operation
	if src.Workers.Operations["operation"].Steps[0].Name != "step" ||
		!src.Workers.Operations["operation"].Finished.Equal(now) {
		t.Fatal("operation references share storage with the source")
	}
	dst.Workers.Histories["worker/production"][0].Revision = 2
	if src.Workers.Histories["worker/production"][0].Revision != 1 {
		t.Fatal("history slice shares storage with the source")
	}
	configuration := dst.Workers.Configurations["configuration"]
	configuration.Variables["nested"].(map[string]any)["value"] = "changed"
	configuration.HyperdriveBindings["DB"] = "changed"
	configuration.R2Bindings["BUCKET"] = WorkerR2Binding{BucketID: "changed"}
	configuration.SecretNames[0] = "changed"
	configuration.SecretVersions["SECRET"] = "changed"
	dst.Workers.Configurations["configuration"] = configuration
	sourceConfiguration := src.Workers.Configurations["configuration"]
	if sourceConfiguration.Variables["nested"].(map[string]any)["value"] != "original" ||
		sourceConfiguration.HyperdriveBindings["DB"] != "hyperdrive" ||
		sourceConfiguration.R2Bindings["BUCKET"].BucketID != "bucket" ||
		sourceConfiguration.SecretNames[0] != "SECRET" ||
		sourceConfiguration.SecretVersions["SECRET"] != "version" {
		t.Fatal("configuration references share storage with the source")
	}
}

func changedOrganization(organization Organization) Organization {
	organization.Sources[0] = "changed"
	return organization
}

func changedGroup(group Group) Group {
	group.Members[0] = "changed"
	return group
}

func changedAssignment(assignment Assignment) Assignment {
	assignment.Conditions.Environments[0] = "changed"
	return assignment
}

func changedBuildPrivate(private WorkerBuildPrivate) WorkerBuildPrivate {
	private.Files["index.js"] = "changed"
	return private
}
