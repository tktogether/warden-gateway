package panel

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/tktogether/warden-gateway/internal/auth"
	"github.com/tktogether/warden-gateway/internal/singbox"
)

// coreJSON flattens the kernel status for the panel.
func coreJSON(status singbox.CoreStatus) map[string]interface{} {
	out := map[string]interface{}{
		"installed":        status.Installed,
		"version":          status.Version,
		"latest":           status.Latest,
		"update_available": status.UpdateAvailable,
		"platform":         status.Platform,
		"binary":           status.Binary,
		"unit_installed":   status.UnitInstalled,
		"unit_managed":     status.UnitManaged,
		"release_url":      status.ReleaseURL,
		"backups":          status.Backups,
	}
	if status.LatestError != "" {
		out["latest_error"] = status.LatestError
	}
	if status.CheckedAt != "" {
		out["checked_at"] = status.CheckedAt
	}
	if status.Note != "" {
		out["note"] = status.Note
	}
	return out
}

type singboxCoreBody struct {
	Action  string `json:"action"`
	Version string `json:"version"`
}

// handleSingboxCore reports and manages the sing-box kernel itself: install,
// update, rollback (previous version or pre-install state), restart and
// uninstall.
func (s *Server) handleSingboxCore(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.guard(w, r); !ok {
		return
	}
	if s.singboxDisabled(w) {
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, coreJSON(s.singbox.CoreStatus(r.Context())))
	case http.MethodPost:
		var body singboxCoreBody
		if err := readJSON(w, r, &body); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		action := strings.TrimSpace(body.Action)
		ctx, cancel := context.WithTimeout(r.Context(), 12*time.Minute)
		defer cancel()
		var (
			status singbox.CoreStatus
			err    error
		)
		switch action {
		case "install", "update":
			version := strings.TrimSpace(body.Version)
			if version == "" {
				version = "latest"
			}
			s.log.Infof("singbox core: install %s requested", version)
			status, err = s.singbox.InstallCore(ctx, version, func(format string, args ...interface{}) {
				s.log.Infof("singbox core: "+format, args...)
			})
		case "rollback-previous":
			status, err = s.singbox.RollbackPrevious(ctx)
		case "rollback-preinstall":
			status, err = s.singbox.RollbackPreinstall(ctx)
		case "restart":
			err = s.singbox.RestartCore(ctx)
			status = s.singbox.CoreStatus(ctx)
		case "uninstall":
			status, err = s.singbox.UninstallCore(ctx)
		default:
			writeError(w, http.StatusBadRequest, "不支持的内核操作："+action)
			return
		}
		if err != nil {
			s.audit("singbox.core", action+" "+body.Version+": "+err.Error(), "fail", auth.ClientIP(r))
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		detail := action
		if body.Version != "" {
			detail += " " + body.Version
		}
		s.audit("singbox.core", detail, "ok", auth.ClientIP(r))
		writeJSON(w, http.StatusOK, coreJSON(status))
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
