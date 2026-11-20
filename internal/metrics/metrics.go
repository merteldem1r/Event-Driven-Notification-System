// Package metrics holds the service's Prometheus registry and the queue metrics read from
// the database (OBS-1, D20).
package metrics

import (
	"context"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"

	"github.com/merteldem1r/Event-Driven-Notification-System/internal/domain"
)

// queryTimeout bounds the queue query of one scrape, so a slow database can't hold /metrics.
const queryTimeout = 2 * time.Second

// NewRegistry returns a registry with the Go runtime and process metrics. Each program makes its
// own instead of using the library's global one (no globals), so tests start from a clean one.
func NewRegistry() *prometheus.Registry {
	reg := prometheus.NewRegistry()
	reg.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	return reg
}

// QueueStats reads the current size of the queue.
type QueueStats interface {
	QueueStats(ctx context.Context) ([]domain.QueueStat, error)
}

// QueueCollector reports the queue's size, counted in the database each time Prometheus reads
// /metrics: the queue is shared by all processes, so no single one could keep the count (D20).
type QueueCollector struct {
	stats  QueueStats
	depth  *prometheus.Desc
	oldest *prometheus.Desc
}

// NewQueueCollector returns a collector that reads the queue through stats.
func NewQueueCollector(stats QueueStats) *QueueCollector {
	return &QueueCollector{
		stats: stats,
		depth: prometheus.NewDesc("notifications_queue_depth",
			"Notifications waiting to be sent (pending, including those waiting for a retry) or being sent (processing).",
			[]string{"channel", "status"}, nil),
		oldest: prometheus.NewDesc("notifications_queue_oldest_due_seconds",
			"How long the oldest notification that is due has been waiting; 0 if none is due. Shows whether sending keeps up.",
			[]string{"channel"}, nil),
	}
}

// Describe sends the descriptions of the two metrics.
func (c *QueueCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.depth
	ch <- c.oldest
}

// Collect counts the queue now. Every channel and status is reported, as 0 when empty, so a
// series never disappears from graphs. If the query fails, the rest of /metrics still works.
func (c *QueueCollector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()
	stats, err := c.stats.QueueStats(ctx)
	if err != nil {
		ch <- prometheus.NewInvalidMetric(c.depth, err)
		return
	}

	type key struct {
		channel domain.Channel
		status  domain.Status
	}
	counts := map[key]int64{}
	oldest := map[domain.Channel]time.Duration{}
	for _, s := range stats {
		counts[key{s.Channel, s.Status}] = s.Count
		if s.Status == domain.StatusPending {
			oldest[s.Channel] = s.OldestDue
		}
	}
	for _, chn := range domain.Channels() {
		for _, st := range []domain.Status{domain.StatusPending, domain.StatusProcessing} {
			ch <- prometheus.MustNewConstMetric(c.depth, prometheus.GaugeValue, float64(counts[key{chn, st}]), string(chn), string(st))
		}
		ch <- prometheus.MustNewConstMetric(c.oldest, prometheus.GaugeValue, oldest[chn].Seconds(), string(chn))
	}
}
