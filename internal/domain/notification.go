// Package domain holds the core types and rules of the notification service.
package domain

import (
	"errors"
	"time"

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
