package api

import (
	"github.com/jk-magic-creations/launchway/internal/forge/hosts"
	"github.com/jk-magic-creations/launchway/internal/forge/signing"
	"github.com/jk-magic-creations/launchway/internal/mail"
	"github.com/jk-magic-creations/launchway/internal/notices"
)

// personFeatures are the parts of the server that are about people and what
// they sign and receive: verified commits and tags (signing.go) and
// notifications (notices.go). Each is nil until it is turned on.
type personFeatures struct {
	Signing *signing.Service // verified commits and tags (schema 61); nil: off
	Notices *notices.Service // notifications for people (schema 60); nil: off
	Mail    *mail.Queue      // the mail queue; nil: no mail server is set
	// NoticeSettings is the installation's mail section, as EnableNotices was given it.
	NoticeSettings NoticeOptions
	// Hosts is the connections to outside Git hosts (internal/forge/hosts); nil: off. It lives here so that
	// the Server's own file stays within its size limit.
	Hosts *hosts.Service
}
