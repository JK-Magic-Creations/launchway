package api

import (
	"context"
	"net/http"
	"strconv"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jk-magic-creations/launchway/internal/engine"
)

type listNotificationsIn struct {
	Limit int `json:"limit,omitempty" jsonschema:"how many of the newest to list, at most 500; default 100"`
}

type notificationsOut struct {
	Notifications []engine.NotificationView `json:"notifications" jsonschema:"newest first; last_error says why a delivery failed"`
}

func listNotifications(ctx context.Context, c *mcpCall, _ *mcp.CallToolRequest, in listNotificationsIn) (notificationsOut, error) {
	path := "/api/v1/notifications"
	if in.Limit > 0 {
		path += "?limit=" + strconv.Itoa(in.Limit)
	}
	out := notificationsOut{Notifications: []engine.NotificationView{}}
	err := c.apiJSON(ctx, "list_notifications", http.MethodGet, path, nil, &out.Notifications)
	return out, err
}
