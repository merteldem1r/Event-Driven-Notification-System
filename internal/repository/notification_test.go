package repository

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/merteldem1r/Event-Driven-Notification-System/internal/database"
	"github.com/merteldem1r/Event-Driven-Notification-System/internal/domain"
)

// testDB is a pool to a throwaway PostgreSQL container, shared by all tests in this package.
var testDB *pgxpool.Pool

func TestMain(m *testing.M) {
	os.Exit(runTests(m))
}

// runTests starts PostgreSQL with our migrations applied, runs the tests, then removes the container.
// It's separate from TestMain so the deferred cleanup runs before os.Exit.
func runTests(m *testing.M) int {
	ctx := context.Background()

	// Same image as docker-compose.yml. The init scripts run in file-name order, like our migrations.
	migrations, err := filepath.Glob("../../migrations/*.up.sql")
	if err != nil || len(migrations) == 0 {
		fmt.Fprintln(os.Stderr, "no migrations found:", err)
		return 1
	}
	ctr, err := postgres.Run(ctx, "postgres:17-alpine",
		postgres.WithInitScripts(migrations...),
		postgres.BasicWaitStrategies(),
	)
	defer func() {
		if err := testcontainers.TerminateContainer(ctr); err != nil {
			fmt.Fprintln(os.Stderr, "terminate container:", err)
		}
	}()
	if err != nil {
		fmt.Fprintln(os.Stderr, "start postgres container (is Docker running?):", err)
		return 1
	}

	url, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		fmt.Fprintln(os.Stderr, "connection string:", err)
		return 1
	}
	testDB, err = database.Connect(ctx, url)
	if err != nil {
		fmt.Fprintln(os.Stderr, "connect:", err)
		return 1
	}
	defer testDB.Close()

	return m.Run()
}

func ptr[T any](v T) *T { return &v }

func TestCreateAndGetByID(t *testing.T) {
	repo := NewNotifications(testDB)
	batchID := uuid.New()

	tests := []struct {
		name string
		in   domain.Notification
	}{
		{"required fields only", domain.Notification{
			Channel: domain.ChannelSMS, Recipient: "+905551234567", Content: "hello", Priority: domain.PriorityHigh,
		}},
		{"with optional fields", domain.Notification{
			BatchID:        &batchID,
			IdempotencyKey: ptr("key-" + uuid.NewString()), // unique per run
			CorrelationID:  ptr("corr-123"),
			Channel:        domain.ChannelEmail, Recipient: "user@example.com", Content: "hi", Priority: domain.PriorityLow,
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			created, err := repo.Create(t.Context(), tt.in)
			if err != nil {
				t.Fatalf("Create: %v", err)
			}

			// Filled in by the database defaults.
			if created.ID == uuid.Nil {
				t.Error("ID is empty")
			}
			if created.Status != domain.StatusPending {
				t.Errorf("Status = %q, want pending", created.Status)
			}
			if created.Attempts != 0 {
				t.Errorf("Attempts = %d, want 0", created.Attempts)
			}
			if created.CreatedAt.IsZero() || created.UpdatedAt.IsZero() || created.NextAttemptAt.IsZero() {
				t.Error("timestamps not set")
			}

			// Everything we sent comes back unchanged.
			want := tt.in
			want.ID, want.Status, want.Attempts = created.ID, created.Status, created.Attempts
			want.NextAttemptAt, want.CreatedAt, want.UpdatedAt = created.NextAttemptAt, created.CreatedAt, created.UpdatedAt
			if !reflect.DeepEqual(created, want) {
				t.Errorf("Create returned\n %+v\nwant\n %+v", created, want)
			}

			got, err := repo.GetByID(t.Context(), created.ID)
			if err != nil {
				t.Fatalf("GetByID: %v", err)
			}
			if !reflect.DeepEqual(got, created) {
				t.Errorf("GetByID returned\n %+v\nwant\n %+v", got, created)
			}
		})
	}
}

func TestGetByIDNotFound(t *testing.T) {
	_, err := NewNotifications(testDB).GetByID(t.Context(), uuid.New())
	if !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("err = %v, want domain.ErrNotFound", err)
	}
}

// The CHECK constraints reject bad values even if Go validation misses them.
func TestCreateRejectsInvalidValues(t *testing.T) {
	valid := domain.Notification{
		Channel: domain.ChannelPush, Recipient: "device-token", Content: "hi", Priority: domain.PriorityNormal,
	}
	badChannel, badPriority := valid, valid
	badChannel.Channel = "fax"
	badPriority.Priority = 9

	tests := []struct {
		name string
		in   domain.Notification
	}{
		{"unknown channel", badChannel},
		{"unknown priority", badPriority},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewNotifications(testDB).Create(t.Context(), tt.in)
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != "23514" { // check_violation
				t.Errorf("err = %v, want a CHECK constraint violation", err)
			}
		})
	}
}
