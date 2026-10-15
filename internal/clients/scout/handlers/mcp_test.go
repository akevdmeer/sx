package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sleuth-io/sx/v2/internal/asset"
	"github.com/sleuth-io/sx/v2/internal/metadata"
	"github.com/sleuth-io/sx/v2/internal/utils"
)

// newMCPMeta builds config-only MCP metadata for a remote http server.
func newMCPMeta(name, url string) *metadata.Metadata {
	return &metadata.Metadata{
		Asset: metadata.Asset{Name: name, Version: "1", Type: asset.TypeMCP},
		MCP:   &metadata.MCPConfig{Transport: "http", URL: url},
	}
}

// mustConfigOnlyZip builds a minimal zip containing only metadata.toml, so
// utils.HasContentFiles reports no content files (config-only install path).
func mustConfigOnlyZip(t *testing.T) []byte {
	t.Helper()
	zipData, err := utils.CreateZipFromContent("metadata.toml", []byte("metadata_version = \"1.0\"\n"))
	if err != nil {
		t.Fatalf("create zip entry: %v", err)
	}
	return zipData
}

// writeRegistry writes a raw m-mcp-servers.json fixture under targetBase.
func writeRegistry(t *testing.T, targetBase, content string) {
	t.Helper()
	if err := os.MkdirAll(targetBase, 0755); err != nil {
		t.Fatalf("mkdir target base: %v", err)
	}
	if err := os.WriteFile(mcpServersPath(targetBase), []byte(content), 0644); err != nil {
		t.Fatalf("write registry fixture: %v", err)
	}
}

// readRegistryDoc parses the registry under targetBase into a generic map.
func readRegistryDoc(t *testing.T, targetBase string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(mcpServersPath(targetBase))
	if err != nil {
		t.Fatalf("read m-mcp-servers.json: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parse m-mcp-servers.json: %v\n%s", err, data)
	}
	return doc
}

// serverEntry returns doc["servers"][name] as an object, failing the test
// when the entry is missing or not an object.
func serverEntry(t *testing.T, doc map[string]any, name string) map[string]any {
	t.Helper()
	servers, ok := doc["servers"].(map[string]any)
	if !ok {
		t.Fatalf("servers section missing or not an object: %v", doc["servers"])
	}
	raw, exists := servers[name]
	if !exists {
		t.Fatalf("servers.%s missing", name)
	}
	entry, ok := raw.(map[string]any)
	if !ok {
		t.Fatalf("servers.%s is not an object: %v", name, raw)
	}
	return entry
}

// hasServerEntry reports whether doc["servers"][name] exists as an object.
func hasServerEntry(doc map[string]any, name string) bool {
	servers, ok := doc["servers"].(map[string]any)
	if !ok {
		return false
	}
	_, ok = servers[name].(map[string]any)
	return ok
}

// entryConfig returns the entry's config object.
func entryConfig(t *testing.T, entry map[string]any, name string) map[string]any {
	t.Helper()
	config, ok := entry["config"].(map[string]any)
	if !ok {
		t.Fatalf("servers.%s.config missing or not an object: %v", name, entry["config"])
	}
	return config
}

// realisticRegistry is a fixture modeled on a real Scout 1.0.70 box: two
// builtins, a plain custom entry, and an OAuth-bound custom entry.
const realisticRegistry = `{
  "servers": {
    "filesystem": { "builtin": true, "tools": [] },
    "playwright": { "builtin": true, "tools": ["browser_close", "browser_navigate"] },
    "dummy": {
      "builtin": false,
      "config": { "name": "dummy", "url": "https://sandbox-agentgateway.example/mcp" },
      "tools": ["ping"]
    },
    "lab": {
      "builtin": false,
      "config": {
        "name": "lab",
        "url": "https://agentgateway.example/mcp",
        "oauthAlias": "user@example.org"
      },
      "tools": ["some_tool"]
    }
  }
}`

func TestInstallMissingFileCreatesRegistry(t *testing.T) {
	targetBase := t.TempDir() // no m-mcp-servers.json yet

	h := NewMCPHandler(newMCPMeta("dummy", "https://sandbox-agentgateway.example/mcp"))
	if err := h.Install(context.Background(), mustConfigOnlyZip(t), targetBase); err != nil {
		t.Fatalf("Install() error = %v", err)
	}

	doc := readRegistryDoc(t, targetBase)
	if _, ok := doc["servers"].(map[string]any); !ok {
		t.Fatalf("top-level servers missing or not an object: %v", doc)
	}
	entry := serverEntry(t, doc, "dummy")
	if entry["builtin"] != false {
		t.Errorf("builtin = %v, want false", entry["builtin"])
	}
	config := entryConfig(t, entry, "dummy")
	if config["name"] != "dummy" {
		t.Errorf("config.name = %v, want \"dummy\"", config["name"])
	}
	if config["url"] != "https://sandbox-agentgateway.example/mcp" {
		t.Errorf("config.url = %v, want the remote URL", config["url"])
	}
	if _, ok := config["type"]; ok {
		t.Errorf("config must not carry a transport/type field, got %v", config)
	}
	tools, ok := entry["tools"].([]any)
	if !ok || len(tools) != 0 {
		t.Errorf("tools = %#v, want empty array", entry["tools"])
	}
}

func TestInstallPreservesUnknownState(t *testing.T) {
	targetBase := t.TempDir()
	// Builtins, an unknown top-level key, and an unrelated custom entry
	// with unknown fields (entry-level and config-level) must all survive.
	writeRegistry(t, targetBase, `{
  "schemaVersion": 2,
  "servers": {
    "filesystem": { "builtin": true, "tools": [] },
    "playwright": { "builtin": true, "tools": ["browser_close"] },
    "lab": {
      "builtin": false,
      "config": {
        "name": "lab",
        "url": "https://agentgateway.example/mcp",
        "oauthAlias": "user@example.org",
        "region": "westeurope"
      },
      "tools": ["some_tool"],
      "lastSeen": "2026-10-01"
    }
  }
}`)

	ctx := context.Background()
	h := NewMCPHandler(newMCPMeta("dummy", "https://sandbox-agentgateway.example/mcp"))
	if err := h.Install(ctx, mustConfigOnlyZip(t), targetBase); err != nil {
		t.Fatalf("Install() error = %v", err)
	}

	doc := readRegistryDoc(t, targetBase)
	if doc["schemaVersion"] != float64(2) {
		t.Errorf("unknown top-level key not preserved: schemaVersion = %v", doc["schemaVersion"])
	}
	if !hasServerEntry(doc, "filesystem") {
		t.Error("builtin filesystem entry must survive the install")
	}
	if !hasServerEntry(doc, "playwright") {
		t.Error("builtin playwright entry must survive the install")
	}
	lab := serverEntry(t, doc, "lab")
	if lab["lastSeen"] != "2026-10-01" {
		t.Errorf("unknown entry field not preserved: lastSeen = %v", lab["lastSeen"])
	}
	labConfig := entryConfig(t, lab, "lab")
	if labConfig["oauthAlias"] != "user@example.org" {
		t.Errorf("oauthAlias not preserved on unrelated entry: %v", labConfig["oauthAlias"])
	}
	if labConfig["region"] != "westeurope" {
		t.Errorf("unknown config field not preserved on unrelated entry: %v", labConfig["region"])
	}
	if labConfig["url"] != "https://agentgateway.example/mcp" {
		t.Errorf("unrelated entry url must not be touched: %v", labConfig["url"])
	}

	// Removing the sx-managed entry must leave all of that state intact.
	h = NewMCPHandler(newMCPMeta("dummy", "https://sandbox-agentgateway.example/mcp"))
	if err := h.Remove(ctx, targetBase); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	doc = readRegistryDoc(t, targetBase)
	servers := doc["servers"].(map[string]any)
	if _, exists := servers["dummy"]; exists {
		t.Error("dummy entry should be gone after remove")
	}
	if doc["schemaVersion"] != float64(2) {
		t.Errorf("unknown top-level key not preserved after remove: %v", doc["schemaVersion"])
	}
	if !hasServerEntry(doc, "filesystem") {
		t.Error("builtin filesystem entry must survive the remove")
	}
	if !hasServerEntry(doc, "playwright") {
		t.Error("builtin playwright entry must survive the remove")
	}
	lab = serverEntry(t, doc, "lab")
	if lab["lastSeen"] != "2026-10-01" {
		t.Errorf("unknown entry field not preserved after remove: %v", lab["lastSeen"])
	}
	entryConfig(t, lab, "lab") // still intact
}

func TestInstallReinstallMergesNotReplaces(t *testing.T) {
	targetBase := t.TempDir()
	writeRegistry(t, targetBase, realisticRegistry)

	// Reinstall "lab" (already present with oauthAlias + populated tools)
	// with a NEW url: sx must update url, preserve oauthAlias and tools.
	h := NewMCPHandler(newMCPMeta("lab", "https://agentgateway.example/mcp/v2"))
	if err := h.Install(context.Background(), mustConfigOnlyZip(t), targetBase); err != nil {
		t.Fatalf("Install() error = %v", err)
	}

	doc := readRegistryDoc(t, targetBase)
	lab := serverEntry(t, doc, "lab")
	if lab["builtin"] != false {
		t.Errorf("builtin = %v, want false", lab["builtin"])
	}
	config := entryConfig(t, lab, "lab")
	if config["url"] != "https://agentgateway.example/mcp/v2" {
		t.Errorf("config.url = %v, want the NEW url on reinstall", config["url"])
	}
	if config["oauthAlias"] != "user@example.org" {
		t.Errorf("config.oauthAlias = %v, want preserved (replace-on-reinstall would break working OAuth)", config["oauthAlias"])
	}
	if config["name"] != "lab" {
		t.Errorf("config.name = %v, want \"lab\"", config["name"])
	}
	tools, ok := lab["tools"].([]any)
	if !ok || len(tools) != 1 || tools[0] != "some_tool" {
		t.Errorf("tools = %#v, want the preserved discovered-tools list [\"some_tool\"]", lab["tools"])
	}

	// Untouched siblings survive the reinstall.
	if !hasServerEntry(doc, "filesystem") {
		t.Error("builtin filesystem entry must survive the reinstall")
	}
	if !hasServerEntry(doc, "dummy") {
		t.Error("sibling custom entry dummy must survive the reinstall")
	}
}

func TestInstallRefusesBuiltinAndNonRemote(t *testing.T) {
	ctx := context.Background()
	zipData := mustConfigOnlyZip(t)

	t.Run("builtin entry is never modified", func(t *testing.T) {
		targetBase := t.TempDir()
		writeRegistry(t, targetBase, realisticRegistry)
		before, _ := os.ReadFile(mcpServersPath(targetBase))

		h := NewMCPHandler(newMCPMeta("filesystem", "https://evil.example/mcp"))
		if err := h.Install(ctx, zipData, targetBase); err == nil {
			t.Fatal("Install() should refuse to modify a builtin server")
		}

		after, err := os.ReadFile(mcpServersPath(targetBase))
		if err != nil {
			t.Fatalf("registry must survive refusal: %v", err)
		}
		if !bytes.Equal(before, after) {
			t.Error("registry must be byte-identical after a refused install")
		}
	})

	t.Run("stdio transport is refused (remote http/sse only)", func(t *testing.T) {
		targetBase := t.TempDir()
		meta := &metadata.Metadata{
			Asset: metadata.Asset{Name: "local-server", Version: "1", Type: asset.TypeMCP},
			MCP:   &metadata.MCPConfig{Transport: "stdio", Command: "node"},
		}
		if err := NewMCPHandler(meta).Install(ctx, zipData, targetBase); err == nil {
			t.Fatal("Install() should refuse non-remote transports")
		}
		if _, err := os.Stat(mcpServersPath(targetBase)); !os.IsNotExist(err) {
			t.Error("a refused install must not create m-mcp-servers.json")
		}
	})

	t.Run("packaged server zip is refused (remote http/sse only)", func(t *testing.T) {
		targetBase := t.TempDir()
		packagedZip, err := utils.CreateZipFromContent("index.js", []byte("export default {};"))
		if err != nil {
			t.Fatalf("build packaged zip: %v", err)
		}
		if err := NewMCPHandler(newMCPMeta("packaged", "https://x.example/mcp")).Install(ctx, packagedZip, targetBase); err == nil {
			t.Fatal("Install() should refuse packaged (content-carrying) MCP zips")
		}
		if _, err := os.Stat(mcpServersPath(targetBase)); !os.IsNotExist(err) {
			t.Error("a refused install must not create m-mcp-servers.json")
		}
	})

	t.Run("missing [mcp] metadata section is refused", func(t *testing.T) {
		targetBase := t.TempDir()
		meta := &metadata.Metadata{
			Asset: metadata.Asset{Name: "nosection", Version: "1", Type: asset.TypeMCP},
		}
		if err := NewMCPHandler(meta).Install(ctx, zipData, targetBase); err == nil {
			t.Fatal("Install() should refuse metadata without an [mcp] section")
		}
	})
}

func TestRemoveCustomDeletesOnlyThatEntry(t *testing.T) {
	targetBase := t.TempDir()
	writeRegistry(t, targetBase, realisticRegistry)

	h := NewMCPHandler(newMCPMeta("dummy", "https://sandbox-agentgateway.example/mcp"))
	if err := h.Remove(context.Background(), targetBase); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}

	doc := readRegistryDoc(t, targetBase)
	servers := doc["servers"].(map[string]any)
	if _, exists := servers["dummy"]; exists {
		t.Error("dummy entry should be removed")
	}
	for _, survivor := range []string{"filesystem", "playwright", "lab"} {
		if _, exists := servers[survivor]; !exists {
			t.Errorf("%s entry should survive the remove of dummy", survivor)
		}
	}
}

func TestRemoveRefusesBuiltin(t *testing.T) {
	targetBase := t.TempDir()
	writeRegistry(t, targetBase, realisticRegistry)
	before, _ := os.ReadFile(mcpServersPath(targetBase))

	h := NewMCPHandler(newMCPMeta("filesystem", ""))
	if err := h.Remove(context.Background(), targetBase); err == nil {
		t.Fatal("Remove() should refuse to remove a builtin server")
	}

	after, err := os.ReadFile(mcpServersPath(targetBase))
	if err != nil {
		t.Fatalf("registry must survive refusal: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Error("registry must be byte-identical after a refused remove")
	}
}

func TestRemoveMissingEntryIsNoOpSuccess(t *testing.T) {
	targetBase := t.TempDir()
	writeRegistry(t, targetBase, realisticRegistry)
	before, _ := os.ReadFile(mcpServersPath(targetBase))

	// "ghost" has no entry in the registry.
	h := NewMCPHandler(newMCPMeta("ghost", "https://ghost.example/mcp"))
	if err := h.Remove(context.Background(), targetBase); err != nil {
		t.Fatalf("Remove() of a missing entry must be a no-op success, got %v", err)
	}

	after, err := os.ReadFile(mcpServersPath(targetBase))
	if err != nil {
		t.Fatalf("read registry: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Error("registry must be untouched when the entry is missing")
	}
}

func TestVerifyLifecycle(t *testing.T) {
	ctx := context.Background()
	targetBase := t.TempDir()
	writeRegistry(t, targetBase, realisticRegistry)
	zipData := mustConfigOnlyZip(t)

	// Not registered yet.
	h := NewMCPHandler(newMCPMeta("fresh", "https://fresh.example/mcp"))
	installed, msg := h.VerifyInstalled(targetBase)
	if installed {
		t.Fatal("VerifyInstalled() should be false before install")
	}
	if msg != "not found in m-mcp-servers.json" {
		t.Errorf("not-found message = %q, want %q", msg, "not found in m-mcp-servers.json")
	}

	// Install → registered.
	if err := h.Install(ctx, zipData, targetBase); err != nil {
		t.Fatalf("Install() error = %v", err)
	}
	installed, msg = h.VerifyInstalled(targetBase)
	if !installed {
		t.Fatalf("VerifyInstalled() should be true after install (message: %s)", msg)
	}
	if msg != "registered in m-mcp-servers.json" {
		t.Errorf("registered message = %q, want %q", msg, "registered in m-mcp-servers.json")
	}

	// URL drift: same name, new expected url → false + mismatch message.
	drifted := NewMCPHandler(newMCPMeta("fresh", "https://fresh.example/mcp/v2"))
	installed, msg = drifted.VerifyInstalled(targetBase)
	if installed {
		t.Fatal("VerifyInstalled() should be false on url drift")
	}
	if !strings.HasPrefix(msg, "url mismatch: ") {
		t.Errorf("drift message = %q, want an \"url mismatch: ...\" message", msg)
	}

	// Without the [mcp] metadata section (how VerifyAssets builds metadata
	// from the lock file), verification degrades to existence/builtin/name
	// and still reports the registered entry as installed.
	metaOnly := &metadata.Metadata{
		Asset: metadata.Asset{Name: "fresh", Version: "1", Type: asset.TypeMCP},
	}
	installed, msg = NewMCPHandler(metaOnly).VerifyInstalled(targetBase)
	if !installed {
		t.Errorf("VerifyInstalled() without [mcp] metadata should still verify, got (%v, %q)", installed, msg)
	}

	// A builtin entry never verifies as an sx-managed server.
	installed, msg = NewMCPHandler(newMCPMeta("filesystem", "https://x.example/mcp")).VerifyInstalled(targetBase)
	if installed {
		t.Error("VerifyInstalled() must not report a builtin as an sx-managed server")
	}
}

func TestJSONCInputDoesNotBreakInstall(t *testing.T) {
	targetBase := t.TempDir()
	writeRegistry(t, targetBase, `{
  // Scout's registry may carry user comments; sx must parse JSONC.
  "servers": {
    "filesystem": { "builtin": true, "tools": [], },
    "lab": {
      "builtin": false,
      "config": { "name": "lab", "url": "https://agentgateway.example/mcp", "oauthAlias": "user@example.org" },
      "tools": ["some_tool"],
    },
  },
}`)

	h := NewMCPHandler(newMCPMeta("dummy", "https://sandbox-agentgateway.example/mcp"))
	if err := h.Install(context.Background(), mustConfigOnlyZip(t), targetBase); err != nil {
		t.Fatalf("Install() on JSONC registry error = %v", err)
	}

	doc := readRegistryDoc(t, targetBase) // plain JSON after the rewrite
	entry := serverEntry(t, doc, "dummy")
	if entry["builtin"] != false {
		t.Errorf("builtin = %v, want false", entry["builtin"])
	}
	lab := serverEntry(t, doc, "lab")
	if entryConfig(t, lab, "lab")["oauthAlias"] != "user@example.org" {
		t.Error("oauthAlias must survive the JSONC rewrite")
	}
}

func TestNewEntryToolsInitializedEmpty(t *testing.T) {
	targetBase := t.TempDir()
	// Entry pre-exists WITHOUT a tools key: reinstall must initialize it
	// to an empty array rather than dropping or nulling it.
	writeRegistry(t, targetBase, `{
  "servers": {
    "bare": {
      "builtin": false,
      "config": { "name": "bare", "url": "https://old.example/mcp" }
    }
  }
}`)

	h := NewMCPHandler(newMCPMeta("bare", "https://new.example/mcp"))
	if err := h.Install(context.Background(), mustConfigOnlyZip(t), targetBase); err != nil {
		t.Fatalf("Install() error = %v", err)
	}

	doc := readRegistryDoc(t, targetBase)
	entry := serverEntry(t, doc, "bare")
	tools, ok := entry["tools"].([]any)
	if !ok {
		t.Fatalf("tools = %#v, want an initialized empty array (not null/missing)", entry["tools"])
	}
	if len(tools) != 0 {
		t.Errorf("tools = %#v, want empty", tools)
	}
	// New entries created from scratch get the same treatment.
	h = NewMCPHandler(newMCPMeta("fresh", "https://fresh.example/mcp"))
	if err := h.Install(context.Background(), mustConfigOnlyZip(t), targetBase); err != nil {
		t.Fatalf("Install() fresh error = %v", err)
	}
	entry = serverEntry(t, readRegistryDoc(t, targetBase), "fresh")
	if tools, ok := entry["tools"].([]any); !ok || len(tools) != 0 {
		t.Errorf("fresh entry tools = %#v, want empty array", entry["tools"])
	}
}

func TestInstallNeverTouchesSettingsFile(t *testing.T) {
	ctx := context.Background()
	targetBase := t.TempDir()
	writeRegistry(t, targetBase, realisticRegistry)
	settingsPath := filepath.Join(targetBase, FileSettings)

	h := NewMCPHandler(newMCPMeta("dummy", "https://sandbox-agentgateway.example/mcp"))
	if err := h.Install(ctx, mustConfigOnlyZip(t), targetBase); err != nil {
		t.Fatalf("Install() error = %v", err)
	}
	if _, err := os.Stat(settingsPath); !os.IsNotExist(err) {
		t.Error("Install() must never create or write m-settings.json")
	}

	if err := h.Remove(ctx, targetBase); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	if _, err := os.Stat(settingsPath); !os.IsNotExist(err) {
		t.Error("Remove() must never create or write m-settings.json")
	}
}
