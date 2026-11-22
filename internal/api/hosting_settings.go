package api

import (
	"fmt"
	"net/http"
	"strconv"
)

// The installation's hosting settings: how big a snapshot of a ref may be and how long a deleted repository stays
// in the trash before it is removed for good. They are kept in the database (hosting_settings), so changing them
// needs no restart and no file; everyone signed in may read them (the page that offers a snapshot says what the limit
// is), an administrator changes them, and each change is in the Logbook.

const (
	settingArchiveMax = "archive_max_mib"
	settingTrashDays  = "trash_retention_days"

	defaultArchiveMaxMiB = 512
	defaultTrashDays     = 30
	maxArchiveMaxMiB     = 65536
	maxTrashDays         = 3650
)

// HostingSettings is what the settings come to.
type HostingSettings struct {
	// ArchiveMaxMiB is the most a snapshot of a ref may hold, in MiB of files before compression.
	ArchiveMaxMiB int `json:"archive_max_mib"`
	// TrashRetentionDays is how long a deleted repository can still be restored.
	TrashRetentionDays int `json:"trash_retention_days"`
}

func (s *Server) hostingSettings() HostingSettings {
	out := HostingSettings{ArchiveMaxMiB: defaultArchiveMaxMiB, TrashRetentionDays: defaultTrashDays}
	if v, ok, err := s.Engine.HostingSetting(settingArchiveMax); err == nil && ok {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			out.ArchiveMaxMiB = n
		}
	}
	if v, ok, err := s.Engine.HostingSetting(settingTrashDays); err == nil && ok {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			out.TrashRetentionDays = n
		}
	}
	return out
}

func (s *Server) settingsRoutes() []route {
	return []route{
		{"GET /api/v1/hosting-settings", signedIn, "", s.getHostingSettings},
		{"PUT /api/v1/hosting-settings", administer, "hosting.settings", s.putHostingSettings},
	}
}

func (s *Server) getHostingSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.hostingSettings())
}

func (s *Server) putHostingSettings(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ArchiveMaxMiB      *int `json:"archive_max_mib"`
		TrashRetentionDays *int `json:"trash_retention_days"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.ArchiveMaxMiB != nil && (*in.ArchiveMaxMiB < 1 || *in.ArchiveMaxMiB > maxArchiveMaxMiB) {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("the largest snapshot is between 1 and %d MiB", maxArchiveMaxMiB))
		return
	}
	if in.TrashRetentionDays != nil && (*in.TrashRetentionDays < 1 || *in.TrashRetentionDays > maxTrashDays) {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("a deleted repository is kept between 1 and %d days", maxTrashDays))
		return
	}
	who := identityFrom(r).User
	if in.ArchiveMaxMiB != nil {
		if err := s.Engine.SetHostingSetting(settingArchiveMax, strconv.Itoa(*in.ArchiveMaxMiB), who); err != nil {
			s.fail(w, err)
			return
		}
	}
	if in.TrashRetentionDays != nil {
		if err := s.Engine.SetHostingSetting(settingTrashDays, strconv.Itoa(*in.TrashRetentionDays), who); err != nil {
			s.fail(w, err)
			return
		}
	}
	out := s.hostingSettings()
	setAudit(r, "hosting", fmt.Sprintf("largest snapshot %d MiB; a deleted repository is kept %d days", out.ArchiveMaxMiB, out.TrashRetentionDays))
	writeJSON(w, http.StatusOK, out)
}
