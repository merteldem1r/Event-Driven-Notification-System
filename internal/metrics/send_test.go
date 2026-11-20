package metrics

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/merteldem1r/Event-Driven-Notification-System/internal/domain"
)

// Every channel and result exists from the start, at 0.
func TestNewSends_StartsAtZero(t *testing.T) {
	reg := prometheus.NewRegistry()
	NewSends(reg)
	if got := testutil.CollectAndCount(reg, "notifications_send_total"); got != 12 {
		t.Errorf("%d send series, want 12 (3 channels x 4 results)", got)
	}
}

func TestSends(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewSends(reg)
	m.SendDone(domain.ChannelSMS, domain.SendSent, 40*time.Millisecond)
	m.SendDone(domain.ChannelSMS, domain.SendSent, 60*time.Millisecond)
	m.SendDone(domain.ChannelSMS, domain.SendRetry, 5*time.Second)
	m.SendDone(domain.ChannelSMS, domain.SendInterrupted, 20*time.Second) // not a provider time
	m.Delivered(domain.ChannelSMS, 90*time.Second)

	want := `
# HELP notifications_send_total Send attempts by how they ended: sent, retry (failed, tried again later), failed (for good), interrupted (shutdown).
# TYPE notifications_send_total counter
notifications_send_total{channel="email",result="failed"} 0
notifications_send_total{channel="email",result="interrupted"} 0
notifications_send_total{channel="email",result="retry"} 0
notifications_send_total{channel="email",result="sent"} 0
notifications_send_total{channel="push",result="failed"} 0
notifications_send_total{channel="push",result="interrupted"} 0
notifications_send_total{channel="push",result="retry"} 0
notifications_send_total{channel="push",result="sent"} 0
notifications_send_total{channel="sms",result="failed"} 0
notifications_send_total{channel="sms",result="interrupted"} 1
notifications_send_total{channel="sms",result="retry"} 1
notifications_send_total{channel="sms",result="sent"} 2
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(want), "notifications_send_total"); err != nil {
		t.Error(err)
	}
	if got := histogramCount(t, reg, "notifications_provider_request_duration_seconds", "sms"); got != 3 {
		t.Errorf("%d provider times for sms, want 3 (the interrupted one left out)", got)
	}
	if got := histogramCount(t, reg, "notifications_delivery_duration_seconds", "sms"); got != 1 {
		t.Errorf("%d delivery times for sms, want 1", got)
	}
}

type fakeLeases struct{ n int64 }

func (f fakeLeases) RecoverExpired(context.Context) (int64, error) { return f.n, nil }

func TestCountRecovered(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewSends(reg)
	leases := m.CountRecovered(fakeLeases{n: 2})
	for range 3 {
		if _, err := leases.RecoverExpired(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if got := testutil.ToFloat64(m.recovered); got != 6 {
		t.Errorf("recovered = %v, want 6", got)
	}
}

// histogramCount returns how many values the histogram name has for channel ch.
func histogramCount(t *testing.T, reg *prometheus.Registry, name, ch string) uint64 {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range families {
		if f.GetName() != name {
			continue
		}
		for _, m := range f.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == "channel" && l.GetValue() == ch {
					return m.GetHistogram().GetSampleCount()
				}
			}
		}
	}
	t.Fatalf("no %s for %s", name, ch)
	return 0
}
