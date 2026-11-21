package engine

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestAuditStreamRedactsAndNeverBreaksTheAction(t *testing.T) {
	var buf bytes.Buffer
	line := auditLineOf(AuditEntry{At: 1700000000123, User: "runner:r1", Action: "mirror.update", Target: "https://bot:CANARYpw1234567@host/r.git",
		Detail: "Authorization: Bearer CANARYbearer123456 and password=CANARYpass99 token=CANARYtok0001", RequestID: "req_1"}, "default")
	b, _ := json.Marshal(line)
	buf.Write(b)
	for _, c := range []string{"CANARYpw", "CANARYbearer", "CANARYpass", "CANARYtok"} {
		if strings.Contains(buf.String(), c) {
			t.Errorf("%s leaked: %s", c, buf.String())
		}
	}
	if line.Time != "2023-11-14T22:13:20.123Z" || line.ActorKind != "runner" || ActorKind("") != "system" || ActorKind("ada") != "person" {
		t.Errorf("%+v", line)
	}
}
