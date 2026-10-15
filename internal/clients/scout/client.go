// Package scout implements the clients.Client interface for Microsoft
// Scout, the Windows desktop agent. It supports the skill asset type
// (global tier under <root>/skills/) and remote-HTTP MCP servers
// (registered in <root>/m-mcp-servers.json — merge-not-replace semantics
// that preserve Scout's oauthAlias bindings and builtins; see
// handlers/constants.go for the tier and marker rationale), and detects
// its root via Scout-specific filesystem markers.
//
// This is an experimental draft client: every detection and install decision
// is logged at debug level to sx.log so field users can diagnose what sx saw.
// Finding sx.log on Windows: sx logs to the platform cache dir reported by
// os.UserCacheDir, which on Windows is %LOCALAPPDATA% — so the log lives at
// %LOCALAPPDATA%\sx\sx.log (i.e. C:\Users\<name>\AppData\Local\sx\sx.log).
// Set SX_CACHE_DIR to relocate it. On Linux it is $XDG_CACHE_HOME/sx/sx.log
// (~/.cache/sx/sx.log by default); on macOS ~/Library/Caches/sx/sx.log.
package scout

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sleuth-io/sx/v2/internal/asset"
	"github.com/sleuth-io/sx/v2/internal/bootstrap"
	"github.com/sleuth-io/sx/v2/internal/clients"
	"github.com/sleuth-io/sx/v2/internal/clients/scout/handlers"
	"github.com/sleuth-io/sx/v2/internal/lockfile"
	"github.com/sleuth-io/sx/v2/internal/logger"
	"github.com/sleuth-io/sx/v2/internal/metadata"
)

// Client implements the clients.Client interface for Microsoft Scout
type Client struct {
	clients.BaseClient
}

// NewClient creates a new Microsoft Scout client
func NewClient() *Client {
	return &Client{
		BaseClient: clients.NewBaseClient(
			clients.ClientIDScout,
			"Microsoft Scout",
			[]asset.Type{
				asset.TypeSkill,
				asset.TypeMCP,
			},
		),
	}
}

// detectRoot resolves the Scout root directory with full diagnostic logging.
//
// Preference order:
//  1. ~/.scout containing at least one Scout marker
//  2. ~/.copilot containing at least one Scout marker (COPILOT_HOME layout)
//
// A candidate that exists without markers does not count: a bare ~/.copilot
// is GitHub Copilot CLI, and Scout must not false-positive on it.
func detectRoot() (string, bool) {
	log := logger.Get()

	home, err := os.UserHomeDir()
	if err != nil {
		log.Debug("scout: detection: home dir resolution failed", "error", err)
		return "", false
	}
	log.Debug("scout: detection: home dir resolved", "home", home)

	candidates := []struct{ label, dir string }{
		{"preferred", handlers.RootScoutDir},
		{"fallback", handlers.RootCopilotDir},
	}

	for _, candidate := range candidates {
		root := filepath.Join(home, candidate.dir)

		stat, statErr := os.Stat(root)
		exists := statErr == nil
		isDir := exists && stat.IsDir()
		if statErr != nil && !os.IsNotExist(statErr) {
			log.Debug("scout: detection: stat root failed", "path", root, "error", statErr)
		}
		log.Debug("scout: detection: candidate root", "signal", candidate.label, "path", root, "exists", exists, "isDir", isDir)

		if !isDir {
			continue
		}

		for _, marker := range handlers.ScoutMarkers {
			markerPath := filepath.Join(root, marker)
			markerStat, markerErr := os.Stat(markerPath)
			present := markerErr == nil
			if markerErr != nil && !os.IsNotExist(markerErr) {
				log.Debug("scout: detection: stat marker failed", "path", markerPath, "error", markerErr)
			}
			log.Debug("scout: detection: marker check", "root", root, "marker", marker, "path", markerPath, "present", present)
			if present {
				isMarkerDir := markerStat != nil && markerStat.IsDir()
				log.Debug("scout: detection: matched", "signal", candidate.label, "root", root, "marker", marker, "markerIsDir", isMarkerDir)
				return root, true
			}
		}
		log.Debug("scout: detection: candidate root has no Scout markers", "signal", candidate.label, "path", root)
	}

	log.Debug("scout: detection: not installed", "home", home, "checkedRoots", handlers.RootScoutDir+","+handlers.RootCopilotDir)
	return "", false
}

// IsInstalled checks if Scout is installed by looking for a Scout root
// (~/.scout preferred, ~/.copilot fallback) that contains Scout markers.
func (c *Client) IsInstalled() bool {
	_, detected := detectRoot()
	return detected
}

// GetVersion returns the Scout version from a plain-text version file under
// the root, if one exists. Scout 1.0.70 does not write one, so this normally
// returns "". Deliberately does not parse m-settings.json.
func (c *Client) GetVersion() string {
	log := logger.Get()

	root, detected := detectRoot()
	if !detected {
		log.Debug("scout: version: client not detected, no version to report")
		return ""
	}

	versionPath := filepath.Join(root, handlers.VersionFile)
	stat, err := os.Stat(versionPath)
	if err != nil {
		if os.IsNotExist(err) {
			log.Debug("scout: version: none found", "path", versionPath)
		} else {
			log.Debug("scout: version: stat failed", "path", versionPath, "error", err)
		}
		return ""
	}
	if stat.IsDir() {
		log.Debug("scout: version: marker is a directory, not a version file", "path", versionPath)
		return ""
	}

	log.Debug("scout: version: examining version file", "path", versionPath)
	data, err := os.ReadFile(versionPath)
	if err != nil {
		log.Debug("scout: version: read failed", "path", versionPath, "error", err)
		return ""
	}
	version := strings.TrimSpace(string(data))
	log.Debug("scout: version: read", "path", versionPath, "content", version)
	return version
}

// determineTargetBase returns the installation root based on scope.
// Scout only supports global scope — repo/path scopes are skipped.
// Global scope requires a detected Scout root: unlike clients such as
// OpenClaw, installing cannot conjure a usable Scout root, because a root
// without Scout markers would not be read by Scout anyway.
func (c *Client) determineTargetBase(scope *clients.InstallScope, _ asset.Type) (string, error) {
	log := logger.Get()

	switch scope.Type {
	case clients.ScopeGlobal:
		root, detected := detectRoot()
		if !detected {
			log.Debug("scout: target: global scope but no detected Scout root")
			return "", fmt.Errorf("Microsoft Scout not detected (no Scout markers under ~/%s or ~/%s)", handlers.RootScoutDir, handlers.RootCopilotDir)
		}
		log.Debug("scout: target: resolved global root", "root", root)
		return root, nil
	case clients.ScopeRepository, clients.ScopePath:
		log.Debug("scout: target: unsupported scope", "scope", scope.Type)
		return "", fmt.Errorf("Microsoft Scout does not support %s-scoped assets (global only)", scope.Type)
	default:
		root, detected := detectRoot()
		if !detected {
			log.Debug("scout: target: unknown scope treated as global, no detected root", "scope", scope.Type)
			return "", fmt.Errorf("Microsoft Scout not detected (no Scout markers under ~/%s or ~/%s)", handlers.RootScoutDir, handlers.RootCopilotDir)
		}
		log.Debug("scout: target: unknown scope treated as global", "scope", scope.Type, "root", root)
		return root, nil
	}
}

// InstallAssets installs assets to Scout using client-specific handlers
func (c *Client) InstallAssets(ctx context.Context, req clients.InstallRequest) (clients.InstallResponse, error) {
	log := logger.Get()

	resp := clients.InstallResponse{
		Results: make([]clients.AssetResult, 0, len(req.Assets)),
	}

	for _, bundle := range req.Assets {
		result := clients.AssetResult{
			AssetName: bundle.Asset.Name,
		}

		targetBase, err := c.determineTargetBase(req.Scope, bundle.Metadata.Asset.Type)
		if err != nil {
			log.Debug("scout: install: skipped, no target base", "asset", bundle.Asset.Name, "error", err)
			result.Status = clients.StatusSkipped
			result.Message = err.Error()
			resp.Results = append(resp.Results, result)
			continue
		}

		if err := os.MkdirAll(targetBase, 0755); err != nil {
			log.Debug("scout: install: mkdir root failed", "root", targetBase, "error", err)
			result.Status = clients.StatusFailed
			result.Error = err
			result.Message = fmt.Sprintf("Failed to create target directory: %v", err)
			resp.Results = append(resp.Results, result)
			continue
		}
		log.Debug("scout: install: root ensured", "root", targetBase, "asset", bundle.Asset.Name)

		var installErr error
		switch bundle.Metadata.Asset.Type {
		case asset.TypeSkill:
			handler := handlers.NewSkillHandler(bundle.Metadata)
			installErr = handler.Install(ctx, bundle.ZipData, targetBase)
		case asset.TypeMCP:
			handler := handlers.NewMCPHandler(bundle.Metadata)
			installErr = handler.Install(ctx, bundle.ZipData, targetBase)
		default:
			log.Debug("scout: install: unsupported asset type", "asset", bundle.Asset.Name, "type", bundle.Metadata.Asset.Type.Key)
			result.Status = clients.StatusSkipped
			result.Message = "Unsupported asset type: " + bundle.Metadata.Asset.Type.Key
			resp.Results = append(resp.Results, result)
			continue
		}

		if installErr != nil {
			result.Status = clients.StatusFailed
			result.Error = installErr
			result.Message = fmt.Sprintf("Installation failed: %v", installErr)
		} else {
			result.Status = clients.StatusSuccess
			switch bundle.Metadata.Asset.Type {
			case asset.TypeMCP:
				result.Message = "Registered in " + filepath.Join(targetBase, handlers.FileMCPServers)
			default:
				result.Message = "Installed to " + filepath.Join(targetBase, handlers.DirSkills, bundle.Asset.Name)
			}
		}

		resp.Results = append(resp.Results, result)
	}

	return resp, nil
}

// UninstallAssets removes assets from Scout
func (c *Client) UninstallAssets(ctx context.Context, req clients.UninstallRequest) (clients.UninstallResponse, error) {
	log := logger.Get()

	resp := clients.UninstallResponse{
		Results: make([]clients.AssetResult, 0, len(req.Assets)),
	}

	for _, a := range req.Assets {
		result := clients.AssetResult{
			AssetName: a.Name,
		}

		targetBase, err := c.determineTargetBase(req.Scope, a.Type)
		if err != nil {
			log.Debug("scout: uninstall: skipped, no target base", "asset", a.Name, "error", err)
			result.Status = clients.StatusSkipped
			result.Message = err.Error()
			resp.Results = append(resp.Results, result)
			continue
		}

		meta := &metadata.Metadata{
			Asset: metadata.Asset{
				Name:    a.Name,
				Type:    a.Type,
				Version: a.Version,
			},
		}

		var uninstallErr error
		switch a.Type {
		case asset.TypeSkill:
			handler := handlers.NewSkillHandler(meta)
			uninstallErr = handler.Remove(ctx, targetBase)
		case asset.TypeMCP:
			handler := handlers.NewMCPHandler(meta)
			uninstallErr = handler.Remove(ctx, targetBase)
		default:
			log.Debug("scout: uninstall: unsupported asset type", "asset", a.Name, "type", a.Type.Key)
			result.Status = clients.StatusSkipped
			result.Message = "Unsupported asset type: " + a.Type.Key
			resp.Results = append(resp.Results, result)
			continue
		}

		if uninstallErr != nil {
			result.Status = clients.StatusFailed
			result.Error = uninstallErr
		} else {
			result.Status = clients.StatusSuccess
			result.Message = "Uninstalled successfully"
		}

		resp.Results = append(resp.Results, result)
	}

	return resp, nil
}

// ListAssets returns all installed skills for a given scope. Skills only:
// MCP servers are registered in m-mcp-servers.json, not as skills
// directories, so they do not appear here (follows the github_copilot
// pattern — the skills listing is not conflated with MCP registrations).
func (c *Client) ListAssets(ctx context.Context, scope *clients.InstallScope) ([]clients.InstalledSkill, error) {
	log := logger.Get()

	targetBase, err := c.determineTargetBase(scope, asset.TypeSkill)
	if err != nil {
		return nil, fmt.Errorf("cannot determine target directory: %w", err)
	}

	installed, err := handlers.SkillOps.ScanInstalled(targetBase)
	if err != nil {
		log.Debug("scout: list: scan failed", "root", targetBase, "error", err)
		return nil, fmt.Errorf("failed to scan installed skills: %w", err)
	}
	log.Debug("scout: list: scanned", "root", targetBase, "skills", len(installed))

	skills := make([]clients.InstalledSkill, 0, len(installed))
	for _, info := range installed {
		skills = append(skills, clients.InstalledSkill{
			Name:        info.Name,
			Description: info.Description,
			Version:     info.Version,
		})
	}

	return skills, nil
}

// ReadSkill reads the content of a specific skill by name
func (c *Client) ReadSkill(ctx context.Context, name string, scope *clients.InstallScope) (*clients.SkillContent, error) {
	log := logger.Get()

	targetBase, err := c.determineTargetBase(scope, asset.TypeSkill)
	if err != nil {
		return nil, fmt.Errorf("cannot determine target directory: %w", err)
	}

	result, err := handlers.SkillOps.ReadPromptContent(targetBase, name, "SKILL.md", func(m *metadata.Metadata) string { return m.Skill.PromptFile })
	if err != nil {
		log.Debug("scout: read skill failed", "root", targetBase, "skill", name, "error", err)
		return nil, err
	}
	log.Debug("scout: read skill", "root", targetBase, "skill", name, "bytes", len(result.Content))

	return &clients.SkillContent{
		Name:        name,
		Description: result.Description,
		Version:     result.Version,
		Content:     result.Content,
		BaseDir:     result.BaseDir,
	}, nil
}

// EnsureAssetSupport is a no-op for Scout: it reads skills natively from
// <root>/skills/ using the same SKILL.md convention sx writes.
func (c *Client) EnsureAssetSupport(_ context.Context, _ *clients.InstallScope) error {
	return nil
}

// GetBootstrapOptions returns no bootstrap options for Scout: the draft
// client installs no hooks and registers no MCP server.
func (c *Client) GetBootstrapOptions(_ context.Context) []bootstrap.Option {
	return nil
}

// GetBootstrapPath returns an empty string: Scout has no bootstrap config.
func (c *Client) GetBootstrapPath() string {
	return ""
}

// InstallBootstrap is a no-op: Scout has no bootstrap infrastructure.
func (c *Client) InstallBootstrap(_ context.Context, _ []bootstrap.Option) error {
	return nil
}

// UninstallBootstrap is a no-op: Scout has no bootstrap infrastructure.
func (c *Client) UninstallBootstrap(_ context.Context, _ []bootstrap.Option) error {
	return nil
}

// ShouldInstall always returns true: Scout has no hook infrastructure, so
// there is no per-session event to dedup against.
func (c *Client) ShouldInstall(_ context.Context) (bool, error) {
	return true, nil
}

// VerifyAssets checks if assets are actually installed on the filesystem
func (c *Client) VerifyAssets(_ context.Context, assets []*lockfile.Asset, scope *clients.InstallScope) []clients.VerifyResult {
	log := logger.Get()

	results := make([]clients.VerifyResult, 0, len(assets))

	for _, a := range assets {
		result := clients.VerifyResult{
			Asset: a,
		}

		targetBase, err := c.determineTargetBase(scope, a.Type)
		if err != nil {
			result.Installed = false
			result.Message = fmt.Sprintf("cannot determine target directory: %v", err)
			log.Debug("scout: verify: skipped, no target base", "asset", a.Name, "error", err)
			results = append(results, result)
			continue
		}

		handler, err := handlers.NewHandler(a.Type, &metadata.Metadata{
			Asset: metadata.Asset{
				Name:    a.Name,
				Version: a.Version,
				Type:    a.Type,
			},
		})
		if err != nil {
			result.Message = err.Error()
			log.Debug("scout: verify: no handler", "asset", a.Name, "error", err)
		} else {
			result.Installed, result.Message = handler.VerifyInstalled(targetBase)
		}

		results = append(results, result)
	}

	return results
}

// ScanInstalledAssets returns no unmanaged assets: the draft client does not
// import pre-existing Scout skills into the vault.
func (c *Client) ScanInstalledAssets(_ context.Context, _ *clients.InstallScope) ([]clients.InstalledAsset, error) {
	return []clients.InstalledAsset{}, nil
}

// GetAssetPath returns the filesystem path to an installed asset
func (c *Client) GetAssetPath(_ context.Context, name string, assetType asset.Type, scope *clients.InstallScope) (string, error) {
	targetBase, err := c.determineTargetBase(scope, assetType)
	if err != nil {
		return "", fmt.Errorf("cannot determine target directory: %w", err)
	}

	switch assetType {
	case asset.TypeSkill:
		return filepath.Join(targetBase, handlers.DirSkills, name), nil
	case asset.TypeMCP:
		// MCP servers have no per-asset directory; the registry file is the
		// closest thing to an install path.
		return filepath.Join(targetBase, handlers.FileMCPServers), nil
	default:
		return "", fmt.Errorf("path not supported for type: %s", assetType)
	}
}

func init() {
	// Auto-register on package import
	clients.Register(NewClient())
}
