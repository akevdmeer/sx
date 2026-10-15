package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/sleuth-io/sx/v2/internal/logger"
	"github.com/sleuth-io/sx/v2/internal/metadata"
	"github.com/sleuth-io/sx/v2/internal/utils"
)

// MCPHandler registers remote-HTTP MCP servers for Scout in
// <root>/m-mcp-servers.json. Scope, merge semantics, and the
// m-settings.json boundary are documented in the package comment
// (constants.go); this file is the mechanism.
type MCPHandler struct {
	metadata *metadata.Metadata
}

// NewMCPHandler creates a new Scout MCP handler
func NewMCPHandler(meta *metadata.Metadata) *MCPHandler {
	return &MCPHandler{metadata: meta}
}

// Install registers the remote MCP server in <targetBase>/m-mcp-servers.json.
//
// Scope guard: remote (http/sse) servers only — stdio transports and
// packaged server zips are refused (their Scout shapes are unobserved;
// see the open questions in constants.go).
//
// Reinstall is MERGE, not replace: when servers[assetName] exists, sx
// manages exactly "builtin" (false), "config.name", and "config.url";
// everything else — "config.oauthAlias" (Scout's OAuth identity binding),
// unknown config keys, the cached "tools" array, and unknown entry keys —
// survives. A builtin entry is never overwritten. A new entry is
// { builtin: false, config: { name, url }, tools: [] } with no
// transport/type field inside config.
func (h *MCPHandler) Install(ctx context.Context, zipData []byte, targetBase string) error {
	log := logger.Get()
	name := h.metadata.Asset.Name
	path := mcpServersPath(targetBase)

	if h.metadata.MCP == nil {
		log.Debug("scout: mcp install: refused, no [mcp] metadata section", "asset", name)
		return fmt.Errorf("asset %q has no [mcp] metadata section; cannot register an MCP server for Scout", name)
	}
	if !h.metadata.MCP.IsRemote() {
		log.Debug("scout: mcp install: refused, non-remote transport (remote http/sse only)", "asset", name, "transport", h.metadata.MCP.Transport)
		return fmt.Errorf("Scout supports remote (http/sse) MCP servers only; %q declares transport %q", name, h.metadata.MCP.Transport)
	}
	if hasContent, err := utils.HasContentFiles(zipData); err != nil {
		log.Debug("scout: mcp install: zip inspection failed", "asset", name, "error", err)
		return fmt.Errorf("failed to inspect zip contents: %w", err)
	} else if hasContent {
		log.Debug("scout: mcp install: refused, packaged server files (remote http/sse only)", "asset", name)
		return fmt.Errorf("Scout supports remote (http/sse) MCP servers only; %q packages server files", name)
	}

	doc, err := loadMCPServersDoc(path)
	if err != nil {
		return err
	}
	servers, err := serversMap(doc)
	if err != nil {
		return err
	}

	url := h.metadata.MCP.URL
	if existing, exists := servers[name]; exists {
		entry, mergeErr := h.mergeServerEntry(name, url, existing)
		if mergeErr != nil {
			return mergeErr
		}
		servers[name] = entry
	} else {
		log.Debug("scout: mcp install: creating new entry", "asset", name, "url", url)
		servers[name] = map[string]any{
			"builtin": false,
			"config":  map[string]any{"name": name, "url": url},
			"tools":   []string{},
		}
	}

	if err := writeMCPServersDoc(path, doc); err != nil {
		return err
	}
	log.Debug("scout: mcp install: registered", "asset", name, "url", url, "file", path)
	return nil
}

// mergeServerEntry merges the sx-managed keys into an existing
// servers[name] value and returns the merged entry. Every other key on
// the entry and inside its config object survives untouched — especially
// config.oauthAlias, whose loss would break working OAuth on a real box.
func (h *MCPHandler) mergeServerEntry(name, url string, existing any) (map[string]any, error) {
	log := logger.Get()

	entry, ok := existing.(map[string]any)
	if !ok {
		log.Debug("scout: mcp install: refused, existing entry is not an object", "asset", name)
		return nil, fmt.Errorf("m-mcp-servers.json: servers.%s is not an object; refusing to overwrite", name)
	}
	if builtin, _ := entry["builtin"].(bool); builtin {
		log.Debug("scout: mcp install: refused, builtin servers are never modified", "asset", name)
		return nil, fmt.Errorf("refusing to modify builtin Scout MCP server %q in m-mcp-servers.json", name)
	}

	preservedEntryKeys := mcpSortedKeysExcept(entry, "builtin", "config", "tools")
	log.Debug("scout: mcp install: merging into existing entry",
		"asset", name,
		"managedKeys", []string{"builtin", "config.name", "config.url"},
		"preservedEntryKeys", preservedEntryKeys)

	// sx owns exactly: builtin, config.name, config.url.
	entry["builtin"] = false

	rawConfig, hasConfig := entry["config"]
	if !hasConfig {
		log.Debug("scout: mcp install: config missing, creating", "asset", name)
		entry["config"] = map[string]any{"name": name, "url": url}
	} else {
		config, ok := rawConfig.(map[string]any)
		if !ok {
			log.Debug("scout: mcp install: refused, existing config is not an object", "asset", name)
			return nil, fmt.Errorf("m-mcp-servers.json: servers.%s.config is not an object; refusing to overwrite", name)
		}
		preservedConfigKeys := mcpSortedKeysExcept(config, "name", "url")
		log.Debug("scout: mcp install: merging config",
			"asset", name,
			"preservedConfigKeys", preservedConfigKeys, // oauthAlias is the one that matters: Scout's OAuth identity binding
		)
		config["name"] = name
		config["url"] = url
	}

	// Scout's cached discovered-tools list: preserve it on reinstall,
	// initialize [] only when the entry has no tools key at all.
	if _, hasTools := entry["tools"]; hasTools {
		log.Debug("scout: mcp install: tools preserved (Scout's cached discovered-tools list)", "asset", name, "tools", entry["tools"])
	} else {
		log.Debug("scout: mcp install: tools missing, initializing empty", "asset", name)
		entry["tools"] = []string{}
	}

	return entry, nil
}

// Remove unregisters the MCP server from <targetBase>/m-mcp-servers.json.
//
// A missing entry (or a missing file) is a no-op success — nothing to
// remove. A builtin entry is refused: sx never removes Scout's own
// servers. Everything else in the registry is preserved, and
// m-settings.json is never touched.
func (h *MCPHandler) Remove(ctx context.Context, targetBase string) error {
	log := logger.Get()
	name := h.metadata.Asset.Name
	path := mcpServersPath(targetBase)

	doc, err := loadMCPServersDoc(path)
	if err != nil {
		return err
	}
	servers, err := serversMap(doc)
	if err != nil {
		return err
	}

	existing, exists := servers[name]
	if !exists {
		log.Debug("scout: mcp uninstall: entry missing, no-op success", "asset", name, "file", path)
		return nil
	}

	entry, ok := existing.(map[string]any)
	if !ok {
		log.Debug("scout: mcp uninstall: refused, existing entry is not an object", "asset", name)
		return fmt.Errorf("m-mcp-servers.json: servers.%s is not an object; refusing to remove", name)
	}
	if builtin, _ := entry["builtin"].(bool); builtin {
		log.Debug("scout: mcp uninstall: refused, builtin servers are never removed", "asset", name)
		return fmt.Errorf("refusing to remove builtin Scout MCP server %q from m-mcp-servers.json", name)
	}

	delete(servers, name)
	if err := writeMCPServersDoc(path, doc); err != nil {
		return err
	}
	log.Debug("scout: mcp uninstall: removed", "asset", name, "file", path)
	return nil
}

// VerifyInstalled reports whether the MCP server is registered in
// <targetBase>/m-mcp-servers.json: the entry exists, is not builtin, and
// config.name matches the asset name. When the expected URL is known
// (full metadata present), a config.url drift is reported as a mismatch;
// without metadata the check degrades to existence, builtin, and name.
func (h *MCPHandler) VerifyInstalled(targetBase string) (bool, string) {
	log := logger.Get()
	name := h.metadata.Asset.Name
	path := mcpServersPath(targetBase)

	doc, err := loadMCPServersDoc(path)
	if err != nil {
		log.Debug("scout: mcp verify: read failed", "asset", name, "file", path, "error", err)
		return false, "failed to read m-mcp-servers.json: " + err.Error()
	}
	servers, err := serversMap(doc)
	if err != nil {
		log.Debug("scout: mcp verify: registry malformed", "asset", name, "file", path, "error", err)
		return false, err.Error()
	}

	existing, exists := servers[name]
	if !exists {
		log.Debug("scout: mcp verify: not found", "asset", name, "file", path)
		return false, "not found in m-mcp-servers.json"
	}

	entry, ok := existing.(map[string]any)
	if !ok {
		log.Debug("scout: mcp verify: entry is not an object", "asset", name)
		return false, fmt.Sprintf("servers.%s in m-mcp-servers.json is not an object", name)
	}
	if builtin, _ := entry["builtin"].(bool); builtin {
		log.Debug("scout: mcp verify: entry is builtin, sx-managed entry not present", "asset", name)
		return false, "builtin Scout MCP server (sx never manages builtins)"
	}

	config, hasConfig := entry["config"].(map[string]any)
	if !hasConfig {
		log.Debug("scout: mcp verify: config section missing", "asset", name)
		return false, "config section missing for " + name + " in m-mcp-servers.json"
	}
	if config["name"] != name {
		log.Debug("scout: mcp verify: name mismatch", "asset", name, "registeredName", config["name"])
		return false, fmt.Sprintf("name mismatch: registered %v, expected %s", config["name"], name)
	}

	// URL drift check only when the expected URL is known: VerifyAssets
	// constructs metadata from the lock file (no [mcp] section), so the
	// check degrades to existence/builtin/name there.
	if h.metadata.MCP != nil && h.metadata.MCP.URL != "" {
		if config["url"] != h.metadata.MCP.URL {
			log.Debug("scout: mcp verify: url mismatch",
				"asset", name,
				"registeredUrl", config["url"],
				"expectedUrl", h.metadata.MCP.URL)
			return false, fmt.Sprintf("url mismatch: registered %v, expected %s", config["url"], h.metadata.MCP.URL)
		}
	}

	log.Debug("scout: mcp verify: registered", "asset", name, "file", path, "url", config["url"])
	return true, "registered in m-mcp-servers.json"
}

// mcpServersPath returns the registry path under the target root.
func mcpServersPath(targetBase string) string {
	return filepath.Join(targetBase, FileMCPServers)
}

// loadMCPServersDoc reads m-mcp-servers.json as a generic object map,
// tolerating JSONC (comments, trailing commas) like the github_copilot
// handler does for its configs. A missing or empty file starts a fresh
// document; the caller decides whether to write it back.
func loadMCPServersDoc(path string) (map[string]any, error) {
	log := logger.Get()

	statErr := func() error {
		_, err := os.Stat(path)
		if err != nil && !os.IsNotExist(err) {
			log.Debug("scout: mcp registry: stat failed", "path", path, "error", err)
		}
		return err
	}()
	if statErr != nil {
		if os.IsNotExist(statErr) {
			log.Debug("scout: mcp registry: file missing, starting fresh", "path", path)
			return map[string]any{}, nil
		}
		return nil, fmt.Errorf("failed to stat m-mcp-servers.json: %w", statErr)
	}
	log.Debug("scout: mcp registry: reading", "path", path)

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read m-mcp-servers.json: %w", err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		log.Debug("scout: mcp registry: file empty, starting fresh", "path", path)
		return map[string]any{}, nil
	}

	var doc map[string]any
	if err := utils.UnmarshalJSONC(data, &doc); err != nil {
		return nil, fmt.Errorf("failed to parse m-mcp-servers.json: %w", err)
	}
	if doc == nil {
		// A literal JSON null parses to a nil map; treat as fresh.
		log.Debug("scout: mcp registry: parsed null document, starting fresh", "path", path)
		doc = map[string]any{}
	}
	log.Debug("scout: mcp registry: read", "path", path, "bytes", len(data), "rootKeys", mcpSortedKeysExcept(doc))
	return doc, nil
}

// serversMap returns the doc's "servers" object, creating an empty one
// (and linking it into the doc) when absent. An existing non-object
// "servers" value is refused rather than clobbered.
func serversMap(doc map[string]any) (map[string]any, error) {
	raw, exists := doc["servers"]
	if !exists {
		servers := map[string]any{}
		doc["servers"] = servers
		return servers, nil
	}
	servers, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("m-mcp-servers.json: \"servers\" is not an object; refusing to overwrite")
	}
	return servers, nil
}

// writeMCPServersDoc writes the registry with 2-space indented JSON,
// atomically (temp file + rename in the same directory) so Scout never
// reads a half-written file. UTF-8 without BOM is Go's default — no
// encoding wrappers. JSONC comments in an existing file do not survive
// the rewrite; unknown keys and entries do.
func writeMCPServersDoc(path string, doc map[string]any) error {
	log := logger.Get()

	if err := utils.EnsureDir(filepath.Dir(path)); err != nil {
		return fmt.Errorf("failed to ensure directory for m-mcp-servers.json: %w", err)
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal m-mcp-servers.json: %w", err)
	}
	if err := utils.WriteFileAtomic(path, data, 0644); err != nil {
		return fmt.Errorf("failed to write m-mcp-servers.json: %w", err)
	}
	log.Debug("scout: mcp registry: written atomically", "path", path, "bytes", len(data))
	return nil
}

// mcpSortedKeysExcept returns the map's keys (minus the excluded ones)
// in sorted order, for deterministic debug logging of generic JSON
// objects. With no exclusions it is just the sorted key list.
func mcpSortedKeysExcept(m map[string]any, exclude ...string) []string {
	keys := make([]string, 0, len(m))
outer:
	for key := range m {
		for _, ex := range exclude {
			if key == ex {
				continue outer
			}
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
