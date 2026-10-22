package handlers

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/sleuth-io/sx/v2/internal/asset"
	"github.com/sleuth-io/sx/v2/internal/handlers/dirasset"
	"github.com/sleuth-io/sx/v2/internal/logger"
	"github.com/sleuth-io/sx/v2/internal/metadata"
)

// SkillOps provides directory-based operations for skill assets
var SkillOps = dirasset.NewOperations(DirSkills, &asset.TypeSkill)

// SkillHandler handles skill asset installation for Scout
type SkillHandler struct {
	metadata *metadata.Metadata
}

// NewSkillHandler creates a new skill handler
func NewSkillHandler(meta *metadata.Metadata) *SkillHandler {
	return &SkillHandler{
		metadata: meta,
	}
}

// Install extracts and installs the skill asset into <targetBase>/skills/<name>/
// and logs the full written tree to sx.log for field diagnostics.
func (h *SkillHandler) Install(ctx context.Context, zipData []byte, targetBase string) error {
	log := logger.Get()
	name := h.metadata.Asset.Name

	skillsDir := filepath.Join(targetBase, DirSkills)
	log.Debug("scout: install: skills dir resolved", "root", targetBase, "skillsDir", skillsDir, "asset", name)

	// Note whether the skills dir already existed before we create it, so
	// the log distinguishes "created" from "already present".
	_, statErr := os.Stat(skillsDir)
	skillsDirExisted := statErr == nil
	if statErr != nil && !os.IsNotExist(statErr) {
		log.Debug("scout: install: stat skills dir failed", "path", skillsDir, "error", statErr)
	}
	if err := os.MkdirAll(skillsDir, 0755); err != nil {
		log.Debug("scout: install: mkdir skills dir failed", "path", skillsDir, "error", err)
		return err
	}
	if skillsDirExisted {
		log.Debug("scout: install: skills dir already present", "path", skillsDir)
	} else {
		log.Debug("scout: install: skills dir created", "path", skillsDir)
	}

	if err := SkillOps.Install(ctx, zipData, targetBase, name); err != nil {
		log.Debug("scout: install: extract failed", "asset", name, "version", h.metadata.Asset.Version, "error", err)
		return err
	}

	// Walk the freshly written tree and log every directory created and
	// every file written (with byte count) for field diagnostics.
	assetDir := filepath.Join(skillsDir, name)
	fileCount := 0
	err := filepath.WalkDir(assetDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			log.Debug("scout: install: walk error", "path", path, "error", walkErr)
			return nil // keep walking what we can; the summary still reports
		}
		if d.IsDir() {
			log.Debug("scout: install: directory written", "path", path)
			return nil
		}
		size := int64(-1)
		if info, infoErr := d.Info(); infoErr == nil {
			size = info.Size()
		}
		fileCount++
		log.Debug("scout: install: file written", "path", path, "bytes", size)
		return nil
	})
	if err != nil {
		log.Debug("scout: install: walk aborted", "assetDir", assetDir, "error", err)
	}

	log.Debug("scout: install: summary", "asset", name, "version", h.metadata.Asset.Version, "files", fileCount, "targetRoot", targetBase)
	return nil
}

// Remove uninstalls the skill asset from <targetBase>/skills/<name>/
func (h *SkillHandler) Remove(ctx context.Context, targetBase string) error {
	log := logger.Get()
	name := h.metadata.Asset.Name

	assetDir := filepath.Join(targetBase, DirSkills, name)
	if _, statErr := os.Stat(assetDir); statErr != nil {
		log.Debug("scout: uninstall: asset dir not present, nothing to remove", "path", assetDir, "error", statErr)
	} else {
		log.Debug("scout: uninstall: asset dir present, removing", "path", assetDir)
	}

	if err := SkillOps.Remove(ctx, targetBase, name); err != nil {
		log.Debug("scout: uninstall: remove failed", "path", assetDir, "error", err)
		return err
	}
	log.Debug("scout: uninstall: removed", "path", assetDir)
	return nil
}

// VerifyInstalled checks if the skill is properly installed
func (h *SkillHandler) VerifyInstalled(targetBase string) (bool, string) {
	log := logger.Get()
	name := h.metadata.Asset.Name

	assetDir := filepath.Join(targetBase, DirSkills, name)
	if _, statErr := os.Stat(assetDir); statErr != nil {
		log.Debug("scout: verify: asset dir stat failed", "path", assetDir, "error", statErr)
	} else {
		log.Debug("scout: verify: asset dir stat ok", "path", assetDir)
	}

	installed, message := SkillOps.VerifyInstalled(targetBase, name, h.metadata.Asset.Version)
	log.Debug("scout: verify: result", "asset", name, "version", h.metadata.Asset.Version, "installed", installed, "message", message)
	return installed, message
}
