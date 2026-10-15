package commands

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sleuth-io/sx/v2/internal/clients"
	_ "github.com/sleuth-io/sx/v2/internal/clients/scout" // Auto-registers via init()
)

// TestScoutNotDetectedWithBareCopilotDir guards the discrimination against
// the github_copilot client: TestEnv creates a bare ~/.copilot (Copilot CLI
// layout, no Scout markers), which must NOT make Scout report installed.
func TestScoutNotDetectedWithBareCopilotDir(t *testing.T) {
	_ = NewTestEnv(t)

	client, err := clients.Global().Get(clients.ClientIDScout)
	if err != nil {
		t.Fatalf("Scout client not registered: %v", err)
	}

	if client.IsInstalled() {
		t.Error("Scout must not be detected from a bare ~/.copilot (that is Copilot CLI, not Scout)")
	}
}

// TestScoutDetectedWithMarkerUnderCopilotRoot verifies Scout is detected when
// Scout markers exist under the ~/.copilot root (COPILOT_HOME layout).
func TestScoutDetectedWithMarkerUnderCopilotRoot(t *testing.T) {
	env := NewTestEnv(t)

	if err := os.MkdirAll(filepath.Join(env.HomeDir, ".copilot", "m-skills"), 0755); err != nil {
		t.Fatalf("Failed to create Scout marker under ~/.copilot: %v", err)
	}

	client, err := clients.Global().Get(clients.ClientIDScout)
	if err != nil {
		t.Fatalf("Scout client not registered: %v", err)
	}

	if !client.IsInstalled() {
		t.Error("Scout should be detected when ~/.copilot contains a Scout marker")
	}
}

// TestScoutClientInfo verifies Scout appears in client info as installed when
// a marker exists under ~/.scout.
func TestScoutClientInfo(t *testing.T) {
	env := NewTestEnv(t)
	setupTestConfig(t, env.HomeDir, nil, nil)

	scoutDir := filepath.Join(env.HomeDir, ".scout")
	if err := os.MkdirAll(filepath.Join(scoutDir, "m-skills"), 0755); err != nil {
		t.Fatalf("Failed to create ~/.scout with marker: %v", err)
	}

	infos := gatherClientInfo()

	var found bool
	for _, info := range infos {
		if info.ID == clients.ClientIDScout {
			found = true
			if !info.Installed {
				t.Error("Scout should show as installed with a marker under ~/.scout")
			}
			if info.Name != "Microsoft Scout" {
				t.Errorf("Expected display name 'Microsoft Scout', got %q", info.Name)
			}
			break
		}
	}

	if !found {
		t.Error("Scout not found in client info")
	}
}
