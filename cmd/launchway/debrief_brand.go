package main

import (
	"log/slog"

	"github.com/jk-magic-creations/launchway/internal/api"
	"github.com/jk-magic-creations/launchway/internal/config"
)

// setDebrief gives the server the name, logo and coverage floor for exported
// Debriefs. A setting that cannot be used (a logo that is not a PNG or JPEG,
// say) is reported loudly and the neutral default is used instead, so a
// branding mistake never keeps the server from starting.
func setDebrief(srv *api.Server, cfg *config.Config, log *slog.Logger) {
	name, logo, floor, err := cfg.DebriefBrand()
	if err != nil {
		log.Error("the Debrief branding in the configuration was not used; exports are neutral", "err", err)
		name, logo, floor = "", "", 0
	}
	srv.SetDebrief(api.DebriefSettings{Name: name, Logo: logo, ChangeFloor: floor})
}
