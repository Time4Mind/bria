package nodecontrol

import (
	"context"
	"testing"
	"time"
)

func TestHeartbeatRetryBackoffIsBoundedAndResets(t *testing.T) {
	agent, err := NewHeartbeatAgent(
		staticLeadership("leader"), &heartbeatPublisherRecorder{},
		func(context.Context) (Heartbeat, error) {
			return Heartbeat{NodeID: "follower", BootID: "boot"}, nil
		},
		5*time.Second,
	)
	if err != nil {
		t.Fatal(err)
	}
	retry := agent.interval
	want := []time.Duration{5, 10, 20, 40, 60, 60}
	for index, seconds := range want {
		delay, next := heartbeatRetryDelay(retry, agent.interval, agent.maxRetry, true)
		if delay != seconds*time.Second {
			t.Fatalf("delay %d=%s, want %ds", index, delay, seconds)
		}
		retry = next
	}
	delay, retry := heartbeatRetryDelay(retry, agent.interval, agent.maxRetry, false)
	if delay != 5*time.Second || retry != 5*time.Second {
		t.Fatalf("successful delay=%s retry=%s", delay, retry)
	}
}
