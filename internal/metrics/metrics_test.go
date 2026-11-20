package metrics

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/merteldem1r/Event-Driven-Notification-System/internal/domain"
)

type fakeStats struct {
	stats []domain.QueueStat
	err   error
}

func (f fakeStats) QueueStats(context.Context) ([]domain.QueueStat, error) { return f.stats, f.err }

// Every channel and status appears, with 0 where the database had no rows.
func TestQueueCollector(t *testing.T) {
	c := NewQueueCollector(fakeStats{stats: []domain.QueueStat{
		{Channel: domain.ChannelSMS, Status: domain.StatusPending, Count: 1250, OldestDue: 90 * time.Second},
		{Channel: domain.ChannelSMS, Status: domain.StatusProcessing, Count: 30},
		{Channel: domain.ChannelEmail, Status: domain.StatusPending, Count: 4},
	}})
	want := `
# HELP notifications_queue_depth Notifications waiting to be sent (pending, including those waiting for a retry) or being sent (processing).
# TYPE notifications_queue_depth gauge
notifications_queue_depth{channel="email",status="pending"} 4
notifications_queue_depth{channel="email",status="processing"} 0
notifications_queue_depth{channel="push",status="pending"} 0
notifications_queue_depth{channel="push",status="processing"} 0
notifications_queue_depth{channel="sms",status="pending"} 1250
notifications_queue_depth{channel="sms",status="processing"} 30
# HELP notifications_queue_oldest_due_seconds How long the oldest notification that is due has been waiting; 0 if none is due. Shows whether sending keeps up.
# TYPE notifications_queue_oldest_due_seconds gauge
notifications_queue_oldest_due_seconds{channel="email"} 0
notifications_queue_oldest_due_seconds{channel="push"} 0
notifications_queue_oldest_due_seconds{channel="sms"} 90
`
	if err := testutil.CollectAndCompare(c, strings.NewReader(want)); err != nil {
		t.Error(err)
	}
}

// A failed query is reported as an error for this collector only.
func TestQueueCollector_QueryError(t *testing.T) {
	reg := prometheus.NewRegistry()
	reg.MustRegister(NewQueueCollector(fakeStats{err: errors.New("database down")}))
	if _, err := reg.Gather(); err == nil || !strings.Contains(err.Error(), "database down") {
		t.Errorf("Gather error = %v, want the database error", err)
	}
}

// The registry already has the Go runtime metrics, and is a new one each time.
func TestNewRegistry(t *testing.T) {
	families, err := NewRegistry().Gather()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range families {
		found = found || f.GetName() == "go_goroutines"
	}
	if !found {
		t.Error("go_goroutines missing")
	}
	NewRegistry().MustRegister(NewQueueCollector(fakeStats{})) // would panic on a shared registry
	NewRegistry().MustRegister(NewQueueCollector(fakeStats{}))
}
