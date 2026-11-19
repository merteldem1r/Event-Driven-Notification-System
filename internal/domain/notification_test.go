package domain

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

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

		// All problems are reported together.
		{"several errors at once", func(n *Notification) {
			n.Recipient, n.Content, n.Priority = "", "", 0
		}, []string{"recipient", "content", "priority"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			n := Notification{Channel: ChannelSMS, Recipient: "+905551234567", Content: "hello", Priority: PriorityNormal}
			tt.change(&n)

			err := n.Validate()
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
