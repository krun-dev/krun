package storage

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestWorkerCompatibilityEmptyFlagsSurviveStateClone(t *testing.T) {
	state := newState()
	created := now()
	state.Workers.Builds["build"] = WorkerBuild{ID: "build", CompatibilityFlags: []string{}, Created: created}
	state.Workers.Artifacts["artifact"] = WorkerArtifact{ID: "artifact", CompatibilityFlags: []string{}, Created: created}
	state.Workers.Releases["release"] = WorkerRelease{ID: "release", CompatibilityFlags: []string{}, Variables: map[string]string{}, SecretVersions: map[string]string{}, Created: created}

	cloned, err := cloneState(state)
	if err != nil {
		t.Fatal(err)
	}
	if !sameBuild(state.Workers.Builds["build"], cloned.Workers.Builds["build"]) {
		t.Fatal("empty build compatibility flags changed snapshot identity")
	}
	if !sameArtifact(state.Workers.Artifacts["artifact"], cloned.Workers.Artifacts["artifact"]) ||
		!sameArtifactContent(state.Workers.Artifacts["artifact"], cloned.Workers.Artifacts["artifact"]) {
		t.Fatal("empty artifact compatibility flags changed snapshot identity")
	}
	if !sameRelease(state.Workers.Releases["release"], cloned.Workers.Releases["release"]) {
		t.Fatal("empty release compatibility flags changed snapshot identity")
	}
}

func TestWriteReleasesRejectsExistingDescriptorMutation(t *testing.T) {
	before := newWorkerState()
	after := newWorkerState()
	release := WorkerRelease{
		ID: "release", Artifact: "artifact", Profile: "workerd-bundle-v2",
		CompatibilityDate: "2026-09-01", Variables: map[string]string{}, Created: now(),
	}
	before.Releases[release.ID] = release
	release.CompatibilityDate = "2026-09-27"
	after.Releases[release.ID] = release
	if err := writeReleases(context.Background(), nil, before, after); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("existing release descriptor mutation was accepted: %v", err)
	}
}

func TestSameWorkerPlacementUsesInstantEquality(t *testing.T) {
	created := time.Date(2026, time.October, 29, 3, 4, 5, 6, time.FixedZone("test", 8*60*60))
	placement := WorkerPlacement{Service: "service", Organization: "org", NetworkProfile: "default", Pool: "pool", Role: "primary", State: "active", Revision: 1, Created: created, Updated: created}
	other := placement
	other.Created, other.Updated = created.UTC(), created.UTC()
	if !sameWorkerPlacement(placement, other) || !sameWorkerPlacements(map[string]WorkerPlacement{"one": placement}, map[string]WorkerPlacement{"one": other}) {
		t.Fatal("equal placement instants with different locations were treated as different metadata")
	}
	other.Revision++
	if sameWorkerPlacement(placement, other) {
		t.Fatal("changed placement metadata was treated as equal")
	}
}

// catalogConfig models the current deployment: two Gateway instances and two
// domains whose labels are purely descriptive.
