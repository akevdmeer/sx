package scout

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/sleuth-io/sx/v2/internal/asset"
	"github.com/sleuth-io/sx/v2/internal/clients"
	"github.com/sleuth-io/sx/v2/internal/clients/scout/handlers"
	"github.com/sleuth-io/sx/v2/internal/lockfile"
	"github.com/sleuth-io/sx/v2/internal/metadata"
	"github.com/sleuth-io/sx/v2/internal/utils"
)

// setFakeHome points os.UserHomeDir at dir for the duration of the test.
// os.UserHomeDir reads $HOME on Unix and %USERPROFILE% on Windows, so both
// are set (the house pattern in other client tests sets only HOME, which
// works for the Linux CI runners).
func setFakeHome(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("HOME", dir)
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", dir)
	}
}

func TestNewClient(t *testing.T) {
	client := NewClient()

	if client.ID() != clients.ClientIDScout {
		t.Errorf("ID() = %q, want %q", client.ID(), clients.ClientIDScout)
	}

	if client.DisplayName() != "Microsoft Scout" {
		t.Errorf("DisplayName() = %q, want %q", client.DisplayName(), "Microsoft Scout")
	}

	if client.RuleCapabilities() != nil {
		t.Error("RuleCapabilities() should be nil (skills-only client)")
	}
}

func TestSupportsAssetType(t *testing.T) {
	client := NewClient()

	if !client.SupportsAssetType(asset.TypeSkill) {
		t.Error("Scout should support skill assets")
	}

	unsupported := []asset.Type{asset.TypeCommand, asset.TypeAgent, asset.TypeRule, asset.TypeHook, asset.TypeMCP}
	for _, assetType := range unsupported {
		if client.SupportsAssetType(assetType) {
			t.Errorf("Scout should not support %s assets", assetType.Key)
		}
	}
}

func TestDetectRootMatrix(t *testing.T) {
	tests := []struct {
		name           string
		setup          func(home string)
		wantDetected   bool
		wantRootSuffix string // path under the fake home, "" when not detected
	}{
		{
			name:           "scout root with m-skills marker",
			setup:          func(home string) { mkdir(t, filepath.Join(home, handlers.RootScoutDir, "m-skills")) },
			wantDetected:   true,
			wantRootSuffix: handlers.RootScoutDir,
		},
		{
			name: "scout root with m-mcp-servers.json marker file",
			setup: func(home string) {
				writeFile(t, filepath.Join(home, handlers.RootScoutDir, "m-mcp-servers.json"), "{}")
			},
			wantDetected:   true,
			wantRootSuffix: handlers.RootScoutDir,
		},
		{
			name:           "scout root with bundled-skills marker",
			setup:          func(home string) { mkdir(t, filepath.Join(home, handlers.RootScoutDir, "bundled-skills")) },
			wantDetected:   true,
			wantRootSuffix: handlers.RootScoutDir,
		},
		{
			name:           "scout root with copilot marker",
			setup:          func(home string) { mkdir(t, filepath.Join(home, handlers.RootScoutDir, "copilot")) },
			wantDetected:   true,
			wantRootSuffix: handlers.RootScoutDir,
		},
		{
			name:           "bare scout root without markers",
			setup:          func(home string) { mkdir(t, filepath.Join(home, handlers.RootScoutDir)) },
			wantDetected:   false,
			wantRootSuffix: "",
		},
		{
			name:           "copilot root with marker",
			setup:          func(home string) { mkdir(t, filepath.Join(home, handlers.RootCopilotDir, "m-skills")) },
			wantDetected:   true,
			wantRootSuffix: handlers.RootCopilotDir,
		},
		{
			name:           "bare copilot root (Copilot CLI only, no Scout)",
			setup:          func(home string) { mkdir(t, filepath.Join(home, handlers.RootCopilotDir)) },
			wantDetected:   false,
			wantRootSuffix: "",
		},
		{
			name:           "neither root exists",
			setup:          func(home string) {},
			wantDetected:   false,
			wantRootSuffix: "",
		},
		{
			name: "both roots with markers prefers scout",
			setup: func(home string) {
				mkdir(t, filepath.Join(home, handlers.RootScoutDir, "m-skills"))
				mkdir(t, filepath.Join(home, handlers.RootCopilotDir, "m-skills"))
			},
			wantDetected:   true,
			wantRootSuffix: handlers.RootScoutDir,
		},
		{
			name:           "scout root is a file, not a directory",
			setup:          func(home string) { writeFile(t, filepath.Join(home, handlers.RootScoutDir), "not a dir") },
			wantDetected:   false,
			wantRootSuffix: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			setFakeHome(t, home)
			tt.setup(home)

			client := NewClient()
			root, detected := detectRoot()

			if detected != tt.wantDetected {
				t.Errorf("detectRoot() detected = %v, want %v", detected, tt.wantDetected)
			}
			if client.IsInstalled() != tt.wantDetected {
				t.Errorf("IsInstalled() = %v, want %v", client.IsInstalled(), tt.wantDetected)
			}

			wantRoot := ""
			if tt.wantRootSuffix != "" {
				wantRoot = filepath.Join(home, tt.wantRootSuffix)
			}
			if root != wantRoot {
				t.Errorf("detectRoot() root = %q, want %q", root, wantRoot)
			}
		})
	}
}

func TestGetVersion(t *testing.T) {
	t.Run("no root detected", func(t *testing.T) {
		setFakeHome(t, t.TempDir())
		if got := NewClient().GetVersion(); got != "" {
			t.Errorf("GetVersion() = %q, want empty when not detected", got)
		}
	})

	t.Run("root detected but no version file", func(t *testing.T) {
		home := t.TempDir()
		setFakeHome(t, home)
		mkdir(t, filepath.Join(home, handlers.RootScoutDir, "m-skills"))

		if got := NewClient().GetVersion(); got != "" {
			t.Errorf("GetVersion() = %q, want empty when no version file exists", got)
		}
	})

	t.Run("version file present", func(t *testing.T) {
		home := t.TempDir()
		setFakeHome(t, home)
		root := mkdir(t, filepath.Join(home, handlers.RootScoutDir, "m-skills"))
		writeFile(t, filepath.Join(filepath.Dir(root), handlers.VersionFile), "1.0.70\n")

		if got := NewClient().GetVersion(); got != "1.0.70" {
			t.Errorf("GetVersion() = %q, want %q", got, "1.0.70")
		}
	})
}

func TestDetermineTargetBase(t *testing.T) {
	t.Run("global scope with detected root", func(t *testing.T) {
		home := t.TempDir()
		setFakeHome(t, home)
		mkdir(t, filepath.Join(home, handlers.RootScoutDir, "m-skills"))

		got, err := NewClient().determineTargetBase(&clients.InstallScope{Type: clients.ScopeGlobal}, asset.TypeSkill)
		if err != nil {
			t.Fatalf("determineTargetBase() error = %v", err)
		}
		if want := filepath.Join(home, handlers.RootScoutDir); got != want {
			t.Errorf("determineTargetBase() = %q, want %q", got, want)
		}
	})

	t.Run("global scope without detected root", func(t *testing.T) {
		setFakeHome(t, t.TempDir())

		_, err := NewClient().determineTargetBase(&clients.InstallScope{Type: clients.ScopeGlobal}, asset.TypeSkill)
		if err == nil {
			t.Fatal("determineTargetBase() should error when Scout is not detected")
		}
	})

	t.Run("repo scope unsupported", func(t *testing.T) {
		setFakeHome(t, t.TempDir())

		_, err := NewClient().determineTargetBase(&clients.InstallScope{
			Type:     clients.ScopeRepository,
			RepoRoot: "/tmp/test-repo",
		}, asset.TypeSkill)
		if err == nil {
			t.Fatal("determineTargetBase() should error for repo scope")
		}
	})

	t.Run("path scope unsupported", func(t *testing.T) {
		setFakeHome(t, t.TempDir())

		_, err := NewClient().determineTargetBase(&clients.InstallScope{
			Type:     clients.ScopePath,
			RepoRoot: "/tmp/test-repo",
			Path:     "subdir",
		}, asset.TypeSkill)
		if err == nil {
			t.Fatal("determineTargetBase() should error for path scope")
		}
	})
}

// buildSkillZip creates a valid skill zip: SKILL.md + metadata.toml.
func buildSkillZip(t *testing.T, name, version, promptContent string) []byte {
	t.Helper()

	zipData, err := utils.CreateZipFromContent("SKILL.md", []byte(promptContent))
	if err != nil {
		t.Fatalf("Failed to create zip: %v", err)
	}

	meta := "[asset]\n" +
		"name = \"" + name + "\"\n" +
		"version = \"" + version + "\"\n" +
		"type = \"skill\"\n" +
		"description = \"Test skill for Scout\"\n\n" +
		"[skill]\n" +
		"prompt-file = \"SKILL.md\"\n"

	zipData, err = utils.AddFileToZip(zipData, "metadata.toml", []byte(meta))
	if err != nil {
		t.Fatalf("Failed to add metadata to zip: %v", err)
	}
	return zipData
}

func TestInstallRemoveVerifyFlow(t *testing.T) {
	home := t.TempDir()
	setFakeHome(t, home)
	root := mkdir(t, filepath.Join(home, handlers.RootScoutDir, "m-skills"))
	// m-skills is the marker dir; skills must be a sibling, not inside it.
	root = filepath.Dir(root)

	client := NewClient()
	ctx := context.Background()
	scope := &clients.InstallScope{Type: clients.ScopeGlobal}

	skillZip := buildSkillZip(t, "test-skill", "1.0.0", "You are test-skill")

	req := clients.InstallRequest{
		Assets: []*clients.AssetBundle{
			{
				Asset: &lockfile.Asset{
					Name:    "test-skill",
					Version: "1.0.0",
					Type:    asset.TypeSkill,
				},
				Metadata: &metadata.Metadata{
					Asset: metadata.Asset{
						Name:    "test-skill",
						Version: "1.0.0",
						Type:    asset.TypeSkill,
					},
				},
				ZipData: skillZip,
			},
		},
		Scope: scope,
	}

	resp, err := client.InstallAssets(ctx, req)
	if err != nil {
		t.Fatalf("InstallAssets() error = %v", err)
	}
	if len(resp.Results) != 1 {
		t.Fatalf("Expected 1 install result, got %d", len(resp.Results))
	}
	if resp.Results[0].Status != clients.StatusSuccess {
		t.Errorf("Install status = %v, want success (message: %s)", resp.Results[0].Status, resp.Results[0].Message)
	}

	// SKILL.md must land under <root>/skills/test-skill/, never m-skills/
	skillMD := filepath.Join(root, handlers.DirSkills, "test-skill", "SKILL.md")
	content, err := os.ReadFile(skillMD)
	if err != nil {
		t.Fatalf("Failed to read installed SKILL.md: %v", err)
	}
	if string(content) != "You are test-skill" {
		t.Errorf("Installed SKILL.md content = %q, want %q", string(content), "You are test-skill")
	}
	if _, err := os.Stat(filepath.Join(root, "m-skills", "test-skill")); err == nil {
		t.Error("Skill must NOT be installed into m-skills/ (cloud-synced tier)")
	}

	// VerifyAssets
	verifyResults := client.VerifyAssets(ctx, []*lockfile.Asset{req.Assets[0].Asset}, scope)
	if len(verifyResults) != 1 {
		t.Fatalf("Expected 1 verify result, got %d", len(verifyResults))
	}
	if !verifyResults[0].Installed {
		t.Errorf("VerifyAssets() Installed = false, want true (message: %s)", verifyResults[0].Message)
	}

	// ListAssets
	skills, err := client.ListAssets(ctx, scope)
	if err != nil {
		t.Fatalf("ListAssets() error = %v", err)
	}
	if len(skills) != 1 || skills[0].Name != "test-skill" {
		t.Errorf("ListAssets() = %v, want one skill named test-skill", skills)
	}

	// ReadSkill
	skillContent, err := client.ReadSkill(ctx, "test-skill", scope)
	if err != nil {
		t.Fatalf("ReadSkill() error = %v", err)
	}
	if skillContent.Content != "You are test-skill" {
		t.Errorf("ReadSkill() content = %q, want %q", skillContent.Content, "You are test-skill")
	}

	// GetAssetPath
	assetPath, err := client.GetAssetPath(ctx, "test-skill", asset.TypeSkill, scope)
	if err != nil {
		t.Fatalf("GetAssetPath() error = %v", err)
	}
	if want := filepath.Join(root, handlers.DirSkills, "test-skill"); assetPath != want {
		t.Errorf("GetAssetPath() = %q, want %q", assetPath, want)
	}

	// Uninstall
	uninstallResp, err := client.UninstallAssets(ctx, clients.UninstallRequest{
		Assets: []asset.Asset{
			{Name: "test-skill", Version: "1.0.0", Type: asset.TypeSkill},
		},
		Scope: scope,
	})
	if err != nil {
		t.Fatalf("UninstallAssets() error = %v", err)
	}
	if len(uninstallResp.Results) != 1 {
		t.Fatalf("Expected 1 uninstall result, got %d", len(uninstallResp.Results))
	}
	if uninstallResp.Results[0].Status != clients.StatusSuccess {
		t.Errorf("Uninstall status = %v, want success", uninstallResp.Results[0].Status)
	}
	if _, err := os.Stat(skillMD); !os.IsNotExist(err) {
		t.Error("Installed SKILL.md should be removed after uninstall")
	}

	// Verify again: not installed
	verifyResults = client.VerifyAssets(ctx, []*lockfile.Asset{req.Assets[0].Asset}, scope)
	if len(verifyResults) != 1 || verifyResults[0].Installed {
		t.Error("VerifyAssets() should report not installed after uninstall")
	}
}

func TestInstallSkipsUndetectedAndUnsupportedScopes(t *testing.T) {
	ctx := context.Background()
	client := NewClient()
	skillZip := buildSkillZip(t, "test-skill", "1.0.0", "You are test-skill")

	bundle := &clients.AssetBundle{
		Asset: &lockfile.Asset{Name: "test-skill", Version: "1.0.0", Type: asset.TypeSkill},
		Metadata: &metadata.Metadata{
			Asset: metadata.Asset{Name: "test-skill", Version: "1.0.0", Type: asset.TypeSkill},
		},
		ZipData: skillZip,
	}

	t.Run("global scope without detected root is skipped", func(t *testing.T) {
		setFakeHome(t, t.TempDir())

		resp, err := client.InstallAssets(ctx, clients.InstallRequest{
			Assets: []*clients.AssetBundle{bundle},
			Scope:  &clients.InstallScope{Type: clients.ScopeGlobal},
		})
		if err != nil {
			t.Fatalf("InstallAssets() error = %v", err)
		}
		if resp.Results[0].Status != clients.StatusSkipped {
			t.Errorf("Install status = %v, want skipped when Scout not detected", resp.Results[0].Status)
		}
	})

	t.Run("repo scope is skipped", func(t *testing.T) {
		home := t.TempDir()
		setFakeHome(t, home)
		mkdir(t, filepath.Join(home, handlers.RootScoutDir, "m-skills"))

		resp, err := client.InstallAssets(ctx, clients.InstallRequest{
			Assets: []*clients.AssetBundle{bundle},
			Scope: &clients.InstallScope{
				Type:     clients.ScopeRepository,
				RepoRoot: t.TempDir(),
			},
		})
		if err != nil {
			t.Fatalf("InstallAssets() error = %v", err)
		}
		if resp.Results[0].Status != clients.StatusSkipped {
			t.Errorf("Install status = %v, want skipped for repo scope", resp.Results[0].Status)
		}
	})
}

func TestBootstrapDefaults(t *testing.T) {
	client := NewClient()
	ctx := context.Background()

	if opts := client.GetBootstrapOptions(ctx); opts != nil {
		t.Errorf("GetBootstrapOptions() = %v, want nil", opts)
	}
	if path := client.GetBootstrapPath(); path != "" {
		t.Errorf("GetBootstrapPath() = %q, want empty", path)
	}
	if err := client.InstallBootstrap(ctx, nil); err != nil {
		t.Errorf("InstallBootstrap() error = %v, want nil", err)
	}
	if err := client.UninstallBootstrap(ctx, nil); err != nil {
		t.Errorf("UninstallBootstrap() error = %v, want nil", err)
	}
	if should, err := client.ShouldInstall(ctx); err != nil || !should {
		t.Errorf("ShouldInstall() = (%v, %v), want (true, nil)", should, err)
	}
	if err := client.EnsureAssetSupport(ctx, nil); err != nil {
		t.Errorf("EnsureAssetSupport() error = %v, want nil", err)
	}
}

func TestScanInstalledAssets(t *testing.T) {
	home := t.TempDir()
	setFakeHome(t, home)
	mkdir(t, filepath.Join(home, handlers.RootScoutDir, "m-skills"))

	client := NewClient()
	assets, err := client.ScanInstalledAssets(context.Background(), &clients.InstallScope{Type: clients.ScopeGlobal})
	if err != nil {
		t.Fatalf("ScanInstalledAssets() error = %v", err)
	}
	if len(assets) != 0 {
		t.Errorf("ScanInstalledAssets() = %v, want empty (draft client does not import)", assets)
	}
}

func mkdir(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(path, 0755); err != nil {
		t.Fatalf("Failed to create dir %s: %v", path, err)
	}
	return path
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("Failed to create parent dir for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write file %s: %v", path, err)
	}
}
