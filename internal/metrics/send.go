package metrics

import (
	"context"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/merteldem1r/Event-Driven-Notification-System/internal/domain"
)

// Sends holds the worker's metrics: how sends end, how long they take, and recovered leases (D20).
type Sends struct {
	results   *prometheus.CounterVec
	provider  *prometheus.HistogramVec
	delivery  *prometheus.HistogramVec
	recovered prometheus.Counter
}

// NewSends creates the worker's metrics in reg. Every channel and result starts at 0, so rates
// are right from the first send and a missing series never means "no data".
func NewSends(reg prometheus.Registerer) *Sends {
	m := &Sends{
		results: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "notifications_send_total",
			Help: "Send attempts by how they ended: sent, retry (failed, tried again later), failed (for good), interrupted (shutdown).",
		}, []string{"channel", "result"}),
		provider: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "notifications_provider_request_duration_seconds",
			Help:    "Time the provider took to answer one send.",
			Buckets: prometheus.DefBuckets, // 5 ms to 10 s; the provider timeout is 5 s
		}, []string{"channel"}),
		delivery: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "notifications_delivery_duration_seconds",
			Help: "Time from creating a notification to it being sent, including queue time and retries.",
			// 100 ms to 30 min: a backlog or retries (minutes, D8) show up here.
			Buckets: []float64{0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120, 300, 600, 1800},
		}, []string{"channel"}),
		recovered: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "notifications_leases_recovered_total",
			Help: "Notifications taken back from workers that stopped without recording a result (expired lease).",
		}),
	}
	reg.MustRegister(m.results, m.provider, m.delivery, m.recovered)
	for _, ch := range domain.Channels() {
		for _, r := range domain.SendResults() {
			m.results.WithLabelValues(string(ch), string(r))
		}
		m.provider.WithLabelValues(string(ch))
		m.delivery.WithLabelValues(string(ch))
	}
	return m
}

// SendDone records one provider call. A call cut off by shutdown says nothing about the
// provider's speed, so its time is left out.
func (m *Sends) SendDone(ch domain.Channel, result domain.SendResult, took time.Duration) {
	m.results.WithLabelValues(string(ch), string(result)).Inc()
	if result != domain.SendInterrupted {
		m.provider.WithLabelValues(string(ch)).Observe(took.Seconds())
	}
}

// Delivered records the time from creation to sent.
func (m *Sends) Delivered(ch domain.Channel, sinceCreated time.Duration) {
	m.delivery.WithLabelValues(string(ch)).Observe(sinceCreated.Seconds())
}

// LeaseRecoverer takes back expired leases (the worker's sweep, RET-5).
type LeaseRecoverer interface {
	RecoverExpired(ctx context.Context) (int64, error)
}

// CountRecovered wraps leases so that every notification it takes back is counted.
func (m *Sends) CountRecovered(leases LeaseRecoverer) LeaseRecoverer {
	return countingLeases{leases: leases, counter: m.recovered}
}

// countingLeases is a LeaseRecoverer that also counts what it recovered.
type countingLeases struct {
	leases  LeaseRecoverer
	counter prometheus.Counter
}

// RecoverExpired recovers through the wrapped LeaseRecoverer and adds the count.
func (c countingLeases) RecoverExpired(ctx context.Context) (int64, error) {
	n, err := c.leases.RecoverExpired(ctx)
	c.counter.Add(float64(n))
	return n, err
}
