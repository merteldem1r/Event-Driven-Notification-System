// Package domain holds the core types and rules of the notification service.
package domain

import (
	"errors"
	"fmt"
	"net/mail"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
)

// ErrNotFound means the requested item doesn't exist.
var ErrNotFound = errors.New("not found")

// Channel is how a notification is delivered.
type Channel string

// Supported channels.
const (
	ChannelSMS   Channel = "sms"
	ChannelEmail Channel = "email"
	ChannelPush  Channel = "push"
)

// Priority decides the processing order; a lower number goes first.
type Priority int16

// Supported priorities. The numbers sort high first in the queue query.
const (
	PriorityHigh   Priority = 1
	PriorityNormal Priority = 2
	PriorityLow    Priority = 3
)

// Status is where a notification is in its lifecycle.
type Status string

// Notification statuses.
const (
	StatusPending    Status = "pending"
	StatusProcessing Status = "processing"
	StatusSent       Status = "sent"
	StatusFailed     Status = "failed"
	StatusCancelled  Status = "cancelled"
)

// Notification is one message to one recipient.
// Pointer fields are optional; nil is stored as NULL.
type Notification struct {
	ID             uuid.UUID
	BatchID        *uuid.UUID
	IdempotencyKey *string
	CorrelationID  *string

	Channel   Channel
	Recipient string
	Content   string
	Priority  Priority

	Status            Status
	Attempts          int
	NextAttemptAt     time.Time
	LastError         *string
	ProviderMessageID *string

	CreatedAt time.Time
	UpdatedAt time.Time
}

// Limits from decision D4, counted in characters, not bytes.
const (
	MaxSMSLength       = 1600    // about 10 SMS segments; the provider splits longer text
	MaxPushLength      = 500     // stays well under the ~4 KB push payload limit
	MaxEmailLength     = 100_000 // safety cap; email itself has no real limit
	MaxPushTokenLength = 512
)

// contentLimits is the maximum content length per channel.
var contentLimits = map[Channel]int{
	ChannelSMS:   MaxSMSLength,
	ChannelEmail: MaxEmailLength,
	ChannelPush:  MaxPushLength,
}

// e164 matches an international phone number: "+", then 8 to 15 digits.
var e164 = regexp.MustCompile(`^\+[1-9][0-9]{7,14}$`)

// FieldError describes one invalid field.
type FieldError struct {
	Field   string
	Message string
}

// ValidationError lists every invalid field, so clients can fix them all at once.
type ValidationError struct {
	Fields []FieldError
}

// Error lists every invalid field in one line.
func (e *ValidationError) Error() string {
	parts := make([]string, len(e.Fields))
	for i, f := range e.Fields {
		parts[i] = f.Field + ": " + f.Message
	}
	return "invalid notification: " + strings.Join(parts, "; ")
}

// Validate checks the fields a client provides and returns a *ValidationError if any are invalid.
func (n Notification) Validate() error {
	var errs []FieldError
	add := func(field, msg string) { errs = append(errs, FieldError{Field: field, Message: msg}) }

	limit, knownChannel := contentLimits[n.Channel]
	if !knownChannel {
		add("channel", "must be sms, email or push")
	}

	switch {
	case strings.TrimSpace(n.Recipient) == "":
		add("recipient", "is required")
	case knownChannel:
		if msg := checkRecipient(n.Channel, n.Recipient); msg != "" {
			add("recipient", msg)
		}
	}

	switch {
	case strings.TrimSpace(n.Content) == "":
		add("content", "is required")
	case knownChannel && utf8.RuneCountInString(n.Content) > limit:
		add("content", fmt.Sprintf("must be at most %d characters for %s", limit, n.Channel))
	}

	if n.Priority < PriorityHigh || n.Priority > PriorityLow {
		add("priority", "must be high, normal or low")
	}

	if len(errs) > 0 {
		return &ValidationError{Fields: errs}
	}
	return nil // a plain nil, not a nil *ValidationError, so err == nil works for callers
}

// checkRecipient returns a message if the recipient doesn't fit the channel, or "" if it does.
func checkRecipient(c Channel, recipient string) string {
	switch c {
	case ChannelSMS:
		if !e164.MatchString(recipient) {
			return "must be a phone number in E.164 format, e.g. +905551234567"
		}
	case ChannelEmail:
		// ParseAddress also accepts "Name <a@b.com>"; requiring an exact match allows only the bare address.
		if addr, err := mail.ParseAddress(recipient); err != nil || addr.Address != recipient {
			return "must be an email address, e.g. user@example.com"
		}
	case ChannelPush:
		if utf8.RuneCountInString(recipient) > MaxPushTokenLength || strings.ContainsFunc(recipient, unicode.IsSpace) {
			return fmt.Sprintf("must be a device token of at most %d characters without spaces", MaxPushTokenLength)
		}
	}
	return ""
}
