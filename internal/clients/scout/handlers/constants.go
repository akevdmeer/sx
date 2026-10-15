// Package handlers holds the filesystem conventions and asset handlers for
// the Scout (Microsoft Scout, Windows desktop agent) client.
package handlers

// Scout filesystem layout.
//
// # Skills tier decision: <root>/skills/ vs <root>/m-skills/
//
// Scout maintains two skills tiers under its root:
//
//   - m-skills/ is the cloud-synced tier. It is managed by Scout's in-memory
//     registry via skills-metadata.json; writing into it from outside risks
//     clobbering registry state (the registry may rewrite or delete entries
//     it does not know about). sx therefore NEVER writes into m-skills/ —
//     that tier is out of scope for this client.
//   - skills/ is the plain directory tier that Scout reads natively, using
//     the same SKILL.md frontmatter convention sx writes for other clients.
//     sx installs skills into <root>/skills/<name>/ only.
//
// Name-collision precedence: when a skill exists in both tiers with the same
// name, m-skills/ wins — a user's app-created (cloud) skill overrides the
// vault one installed by sx. That precedence is acceptable: the user's own
// creation is more specific than the team default, and sx never overwrites
// the winning copy.
//
// # Marker-based root detection and the .copilot discrimination
//
// sx's github_copilot client treats a bare ~/.copilot directory as "GitHub
// Copilot CLI installed". Scout, when configured with COPILOT_HOME pointing
// at ~/.copilot, ALSO stores its state there. To keep the two clients
// distinguishable, Scout detection is marker-based: a candidate root counts
// as Scout's only when it contains at least one entry listed in
// ScoutMarkers. A bare ~/.copilot without markers is Copilot CLI, and Scout
// reports not-installed. (Conversely, ~/.scout with markers is Scout even
// when ~/.copilot also exists; ~/.scout is the preferred root.)
//
// # Version
//
// Scout 1.0.70 does not write a plain-text version file under the root.
// GetVersion checks only for VersionFile and deliberately does NOT parse
// m-settings.json — its format is undocumented, and parsing it would be a
// heuristic that breaks silently on format changes.
//
// # Source basis
//
// Microsoft Learn, "Use Microsoft Scout" and the Scout FAQ (ms.date
// 2026-08-21), plus local build observations from Scout 1.0.70 (Windows
// desktop agent). Treat the names below as observed behavior, not a
// published contract.
//
// Validated end-to-end 2026-10-15 on Scout 1.0.70 (Windows): skills sx
// installs into <root>/skills/ are discovered by Scout. The skills/
// directory holds bundled and user skills side by side. Re-verify on
// Scout updates — the product is in private preview and its layout is a
// moving target.
const (
	// RootScoutDir is the preferred Scout root directory under the home dir.
	RootScoutDir = ".scout"

	// RootCopilotDir is the fallback Scout root under the home dir, used
	// when Scout stores state under COPILOT_HOME (default ~/.copilot).
	RootCopilotDir = ".copilot"

	// DirSkills is the skills subdirectory under the root where sx installs
	// skill assets: <root>/skills/<name>/.
	DirSkills = "skills"

	// VersionFile is the plain-text version marker file checked by
	// GetVersion. No Scout 1.0.70 build has been observed writing one, so
	// GetVersion normally returns "". Update this constant if Microsoft
	// ships a differently named version file. NOT m-settings.json.
	VersionFile = "version"
)

// ScoutMarkers lists filesystem entries (file or directory, directly under a
// candidate root) that prove Scout owns that root. Presence of any single
// marker is sufficient. See the package comment for the discrimination
// rationale against the github_copilot client's bare ~/.copilot check.
var ScoutMarkers = []string{
	"m-mcp-servers.json", // Scout-managed MCP server registry (file)
	"m-skills",           // Scout's cloud-synced skills tier (directory)
	"bundled-skills",     // skills bundled with the Scout app (directory)
	"copilot",            // the COPILOT_HOME dir Scout creates under its root (directory)
}
