package config

import (
	"testing"
	"time"
)

func TestDeployPickupWaitSetting(t *testing.T) {
	for _, c := range []struct {
		in   string
		want time.Duration
		bad  bool
	}{{"", 0, false}, {"30m", 30 * time.Minute, false}, {"off", -1, false}, {"soon", 0, true}, {"-5m", 0, true}} {
		got, err := engineQueues(QueuesConfig{DeployPickupWait: c.in}, "queues")
		if (err != nil) != c.bad || got.DeployPickupWait != c.want {
			t.Errorf("%q: got %v, %v", c.in, got.DeployPickupWait, err)
		}
	}
}
