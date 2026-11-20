package domain

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

// testNow is the "current time" the validation tests run at.
var testNow = time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

func TestValidate(t *testing.T) {
	// Each case starts from a valid SMS and changes what it tests.
	tests := []struct {
		name       string
		change     func(n *Notification)
		wantFields []string // invalid fields in order; nil means valid
	}{
		{"valid sms", func(n *Notification) {}, nil},
		{"valid email", func(n *Notification) { n.Channel, n.Recipient = ChannelEmail, "user@example.com" }, nil},
		{"valid push", func(n *Notification) { n.Channel, n.Recipient = ChannelPush, "fcm_token-123:abc" }, nil},
		{"low priority", func(n *Notification) { n.Priority = PriorityLow }, nil},

		// Content limits per channel.
		{"sms at limit", func(n *Notification) { n.Content = strings.Repeat("a", MaxSMSLength) }, nil},
		{"limit counts characters, not bytes", func(n *Notification) { n.Content = strings.Repeat("ş", MaxSMSLength) }, nil},
		{"sms too long", func(n *Notification) { n.Content = strings.Repeat("a", MaxSMSLength+1) }, []string{"content"}},
		{"push too long", func(n *Notification) {
			n.Channel, n.Recipient, n.Content = ChannelPush, "token", strings.Repeat("a", MaxPushLength+1)
		}, []string{"content"}},
		{"email too long", func(n *Notification) {
			n.Channel, n.Recipient, n.Content = ChannelEmail, "user@example.com", strings.Repeat("a", MaxEmailLength+1)
		}, []string{"content"}},
		{"blank content", func(n *Notification) { n.Content = "  \n " }, []string{"content"}},

		// Channel.
		{"unknown channel", func(n *Notification) { n.Channel = "fax" }, []string{"channel"}},
		{"missing channel", func(n *Notification) { n.Channel = "" }, []string{"channel"}},

		// Recipient per channel.
		{"missing recipient", func(n *Notification) { n.Recipient = " " }, []string{"recipient"}},
		{"sms without plus", func(n *Notification) { n.Recipient = "905551234567" }, []string{"recipient"}},
		{"sms too short", func(n *Notification) { n.Recipient = "+9055" }, []string{"recipient"}},
		{"sms with letters", func(n *Notification) { n.Recipient = "+90555abc4567" }, []string{"recipient"}},
		{"email without @", func(n *Notification) { n.Channel, n.Recipient = ChannelEmail, "user.example.com" }, []string{"recipient"}},
		{"email with display name", func(n *Notification) {
			n.Channel, n.Recipient = ChannelEmail, "User <user@example.com>"
		}, []string{"recipient"}},
		{"push token with space", func(n *Notification) { n.Channel, n.Recipient = ChannelPush, "abc def" }, []string{"recipient"}},
		{"push token too long", func(n *Notification) {
			n.Channel, n.Recipient = ChannelPush, strings.Repeat("a", MaxPushTokenLength+1)
		}, []string{"recipient"}},

		// Priority.
		{"missing priority", func(n *Notification) { n.Priority = 0 }, []string{"priority"}},
		{"unknown priority", func(n *Notification) { n.Priority = 9 }, []string{"priority"}},

		// Idempotency key (optional; nil means no header).
		{"valid idempotency key", func(n *Notification) { n.IdempotencyKey = ptr("order-1042_confirm:v1") }, nil},
		{"key at max length", func(n *Notification) {
			n.IdempotencyKey = ptr(strings.Repeat("k", MaxIdempotencyKeyLength))
		}, nil},
		{"empty key", func(n *Notification) { n.IdempotencyKey = ptr("") }, []string{"Idempotency-Key"}},
		{"key too long", func(n *Notification) {
			n.IdempotencyKey = ptr(strings.Repeat("k", MaxIdempotencyKeyLength+1))
		}, []string{"Idempotency-Key"}},
		{"key with space", func(n *Notification) { n.IdempotencyKey = ptr("order 1042") }, []string{"Idempotency-Key"}},
		{"key with non-ASCII", func(n *Notification) { n.IdempotencyKey = ptr("sipariş-1042") }, []string{"Idempotency-Key"}},

		// Scheduled time (optional; nil means as soon as possible).
		{"scheduled in an hour", func(n *Notification) { n.ScheduledAt = ptr(testNow.Add(time.Hour)) }, nil},
		{"scheduled at the maximum", func(n *Notification) { n.ScheduledAt = ptr(testNow.Add(MaxScheduleAhead)) }, nil},
		{"scheduled in another time zone", func(n *Notification) {
			n.ScheduledAt = ptr(testNow.Add(time.Hour).In(time.FixedZone("+03:00", 3*60*60)))
		}, nil},
		{"scheduled now", func(n *Notification) { n.ScheduledAt = ptr(testNow) }, []string{"scheduled_at"}},
		{"scheduled in the past", func(n *Notification) { n.ScheduledAt = ptr(testNow.Add(-time.Minute)) }, []string{"scheduled_at"}},
		{"scheduled too far ahead", func(n *Notification) {
			n.ScheduledAt = ptr(testNow.Add(MaxScheduleAhead + time.Second))
		}, []string{"scheduled_at"}},

		// All problems are reported together.
		{"several errors at once", func(n *Notification) {
			n.Recipient, n.Content, n.Priority = "", "", 0
		}, []string{"recipient", "content", "priority"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			n := Notification{Channel: ChannelSMS, Recipient: "+905551234567", Content: "hello", Priority: PriorityNormal}
			tt.change(&n)

			err := n.Validate(testNow)
			if tt.wantFields == nil {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}

			var ve *ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("Validate() = %v, want a *ValidationError", err)
			}
			var got []string
			for _, f := range ve.Fields {
				got = append(got, f.Field)
			}
			if !slices.Equal(got, tt.wantFields) {
				t.Errorf("invalid fields = %v, want %v", got, tt.wantFields)
			}
		})
	}
}

func TestValidationErrorMessage(t *testing.T) {
	err := &ValidationError{Fields: []FieldError{
		{Field: "recipient", Message: "is required"},
		{Field: "content", Message: "is required"},
	}}
	want := "invalid notification: recipient: is required; content: is required"
	if got := err.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestPriorityNames(t *testing.T) {
	tests := []struct {
		name string
		p    Priority
	}{
		{"high", PriorityHigh},
		{"normal", PriorityNormal},
		{"low", PriorityLow},
	}
	for _, tt := range tests {
		if got := ParsePriority(tt.name); got != tt.p {
			t.Errorf("ParsePriority(%q) = %d, want %d", tt.name, got, tt.p)
		}
		if got := tt.p.String(); got != tt.name {
			t.Errorf("Priority(%d).String() = %q, want %q", tt.p, got, tt.name)
		}
	}
	if got := ParsePriority("urgent"); got != 0 {
		t.Errorf(`ParsePriority("urgent") = %d, want 0`, got)
	}
}

func ptr[T any](v T) *T { return &v }

func TestSameRequest(t *testing.T) {
	base := Notification{Channel: ChannelSMS, Recipient: "+905551234567", Content: "hello", Priority: PriorityNormal}

	tests := []struct {
		name   string
		change func(n *Notification)
		want   bool
	}{
		{"identical", func(n *Notification) {}, true},
		{"server-set fields are ignored", func(n *Notification) { n.Status, n.Attempts = StatusSent, 3 }, true},
		{"different channel", func(n *Notification) { n.Channel = ChannelPush }, false},
		{"different recipient", func(n *Notification) { n.Recipient = "+905559999999" }, false},
		{"different content", func(n *Notification) { n.Content = "goodbye" }, false},
		{"different priority", func(n *Notification) { n.Priority = PriorityHigh }, false},
		{"scheduled vs not", func(n *Notification) { n.ScheduledAt = ptr(testNow) }, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			other := base
			tt.change(&other)
			if got := base.SameRequest(other); got != tt.want {
				t.Errorf("SameRequest = %v, want %v", got, tt.want)
			}
		})
	}
}

// The same scheduled time is the same request, even after PostgreSQL drops the nanoseconds.
func TestSameRequest_ScheduledTimes(t *testing.T) {
	at := testNow.Add(time.Hour + 1234*time.Nanosecond)
	a := Notification{Channel: ChannelSMS, Recipient: "+905551234567", Content: "hi", Priority: PriorityNormal, ScheduledAt: &at}
	b := a
	b.ScheduledAt = ptr(at.Truncate(time.Microsecond).In(time.FixedZone("+03:00", 3*60*60)))
	if !a.SameRequest(b) {
		t.Error("same time at microsecond precision in another zone: SameRequest = false, want true")
	}
	b.ScheduledAt = ptr(at.Add(time.Second))
	if a.SameRequest(b) {
		t.Error("different scheduled time: SameRequest = true, want false")
	}
}

func TestDueAt(t *testing.T) {
	created := testNow
	tests := []struct {
		name      string
		scheduled *time.Time
		want      time.Time
	}{
		{"not scheduled: due when created", nil, created},
		{"scheduled: due at the scheduled time", ptr(created.Add(time.Hour)), created.Add(time.Hour)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			n := Notification{CreatedAt: created, ScheduledAt: tt.scheduled}
			if got := n.DueAt(); !got.Equal(tt.want) {
				t.Errorf("DueAt = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestValidateBatch(t *testing.T) {
	valid := Notification{Channel: ChannelSMS, Recipient: "+905551234567", Content: "hello", Priority: PriorityNormal}
	noRecipient, noContent := valid, valid
	noRecipient.Recipient = ""
	noContent.Content = ""
	scheduledOK, scheduledPast := valid, valid
	scheduledOK.ScheduledAt = ptr(testNow.Add(time.Hour))
	scheduledPast.ScheduledAt = ptr(testNow.Add(-time.Hour))

	tests := []struct {
		name       string
		batch      []Notification
		key        *string
		wantFields []string // nil means valid
	}{
		{"one valid", []Notification{valid}, nil, nil},
		{"max size", slices.Repeat([]Notification{valid}, MaxBatchSize), nil, nil},
		{"empty", nil, nil, []string{"notifications"}},
		{"too many", slices.Repeat([]Notification{valid}, MaxBatchSize+1), nil, []string{"notifications"}},
		{"invalid items are named by position", []Notification{valid, noRecipient, valid, noContent}, nil,
			[]string{"notifications[1].recipient", "notifications[3].content"}},
		{"valid key", []Notification{valid}, ptr("campaign-7"), nil},
		{"invalid key is reported with the items", []Notification{noRecipient}, ptr("has space"),
			[]string{"notifications[0].recipient", "Idempotency-Key"}},
		{"each item checks its own schedule", []Notification{scheduledOK, scheduledPast}, nil,
			[]string{"notifications[1].scheduled_at"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateBatch(tt.batch, tt.key, testNow)
			if tt.wantFields == nil {
				if err != nil {
					t.Fatalf("ValidateBatch = %v, want nil", err)
				}
				return
			}
			var ve *ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("ValidateBatch = %v, want a *ValidationError", err)
			}
			var got []string
			for _, f := range ve.Fields {
				got = append(got, f.Field)
			}
			if !slices.Equal(got, tt.wantFields) {
				t.Errorf("invalid fields = %v, want %v", got, tt.wantFields)
			}
		})
	}
}

func TestListFilterValidate(t *testing.T) {
	now := time.Now()
	earlier := now.Add(-time.Hour)

	tests := []struct {
		name       string
		filter     ListFilter
		wantFields []string // nil means valid
	}{
		{"defaults only", ListFilter{Limit: DefaultListLimit}, nil},
		{"every filter", ListFilter{
			Status: ptr(StatusFailed), Channel: ptr(ChannelSMS), From: &earlier, To: &now, Limit: MaxListLimit,
		}, nil},
		{"unknown status", ListFilter{Status: ptr(Status("lost")), Limit: 10}, []string{"status"}},
		{"unknown channel", ListFilter{Channel: ptr(Channel("fax")), Limit: 10}, []string{"channel"}},
		{"to before from", ListFilter{From: &now, To: &earlier, Limit: 10}, []string{"to"}},
		{"to equals from", ListFilter{From: &now, To: &now, Limit: 10}, []string{"to"}},
		{"limit 0", ListFilter{Limit: 0}, []string{"limit"}},
		{"limit too big", ListFilter{Limit: MaxListLimit + 1}, []string{"limit"}},
		{"several at once", ListFilter{Status: ptr(Status("lost")), Limit: 0}, []string{"status", "limit"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.filter.Validate()
			if tt.wantFields == nil {
				if err != nil {
					t.Fatalf("Validate = %v, want nil", err)
				}
				return
			}
			var ve *ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("Validate = %v, want a *ValidationError", err)
			}
			var got []string
			for _, f := range ve.Fields {
				got = append(got, f.Field)
			}
			if !slices.Equal(got, tt.wantFields) {
				t.Errorf("invalid fields = %v, want %v", got, tt.wantFields)
			}
		})
	}
}
