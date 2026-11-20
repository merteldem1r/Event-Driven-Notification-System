package repository

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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

func TestCreate_IdempotencyKey(t *testing.T) {
	repo := NewNotifications(testDB)
	n := domain.Notification{
		IdempotencyKey: ptr("key-" + uuid.NewString()),
		Channel:        domain.ChannelSMS, Recipient: "+905551234567", Content: "hello", Priority: domain.PriorityNormal,
	}

	first, err := repo.Create(t.Context(), n)
	if err != nil {
		t.Fatalf("first Create: %v", err)
	}
	if _, err := repo.Create(t.Context(), n); !errors.Is(err, domain.ErrIdempotencyKeyExists) {
		t.Fatalf("second Create err = %v, want domain.ErrIdempotencyKeyExists", err)
	}

	got, err := repo.GetByIdempotencyKey(t.Context(), *n.IdempotencyKey)
	if err != nil {
		t.Fatalf("GetByIdempotencyKey: %v", err)
	}
	if !reflect.DeepEqual(got, first) {
		t.Errorf("GetByIdempotencyKey returned\n %+v\nwant\n %+v", got, first)
	}
}

// Rows without a key never conflict: PostgreSQL doesn't treat NULLs as equal in a UNIQUE column.
func TestCreate_WithoutKeyNeverConflicts(t *testing.T) {
	repo := NewNotifications(testDB)
	n := domain.Notification{
		Channel: domain.ChannelSMS, Recipient: "+905551234567", Content: "hello", Priority: domain.PriorityNormal,
	}
	for i := range 2 {
		if _, err := repo.Create(t.Context(), n); err != nil {
			t.Fatalf("Create #%d: %v", i+1, err)
		}
	}
}

func TestGetByIdempotencyKeyNotFound(t *testing.T) {
	_, err := NewNotifications(testDB).GetByIdempotencyKey(t.Context(), "missing-"+uuid.NewString())
	if !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("err = %v, want domain.ErrNotFound", err)
	}
}

// Many requests with the same key at the same moment: exactly one row is inserted.
func TestCreate_ConcurrentSameKey(t *testing.T) {
	repo := NewNotifications(testDB)
	key := "key-" + uuid.NewString()
	n := domain.Notification{
		IdempotencyKey: &key,
		Channel:        domain.ChannelSMS, Recipient: "+905551234567", Content: "hello", Priority: domain.PriorityNormal,
	}

	const requests = 20
	var inserted, conflicts atomic.Int32
	var wg sync.WaitGroup
	for range requests {
		wg.Go(func() {
			_, err := repo.Create(t.Context(), n)
			switch {
			case err == nil:
				inserted.Add(1)
			case errors.Is(err, domain.ErrIdempotencyKeyExists):
				conflicts.Add(1)
			default:
				t.Errorf("Create: %v", err)
			}
		})
	}
	wg.Wait()

	if inserted.Load() != 1 || conflicts.Load() != requests-1 {
		t.Errorf("inserted = %d, conflicts = %d; want 1 and %d", inserted.Load(), conflicts.Load(), requests-1)
	}
	var rows int
	if err := testDB.QueryRow(t.Context(),
		`SELECT count(*) FROM notifications WHERE idempotency_key = $1`, key).Scan(&rows); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if rows != 1 {
		t.Errorf("rows with the key = %d, want 1", rows)
	}
}

func batchOf(size int, batchID uuid.UUID) []domain.Notification {
	ns := make([]domain.Notification, size)
	for i := range ns {
		ns[i] = domain.Notification{
			BatchID: &batchID, CorrelationID: ptr("corr-batch"),
			Channel: domain.ChannelSMS, Recipient: "+905551234567", Content: fmt.Sprintf("message %d", i),
			Priority: domain.PriorityNormal,
		}
	}
	return ns
}

func countBatch(t *testing.T, batchID uuid.UUID) int {
	t.Helper()
	var n int
	if err := testDB.QueryRow(t.Context(), `SELECT count(*) FROM notifications WHERE batch_id = $1`, batchID).Scan(&n); err != nil {
		t.Fatalf("count batch: %v", err)
	}
	return n
}

func TestCreateBatch(t *testing.T) {
	repo := NewNotifications(testDB)
	batchID := uuid.New()
	in := batchOf(3, batchID)

	created, err := repo.CreateBatch(t.Context(), in)
	if err != nil {
		t.Fatalf("CreateBatch: %v", err)
	}
	if len(created) != len(in) {
		t.Fatalf("got %d notifications, want %d", len(created), len(in))
	}
	for i, n := range created {
		// Same order as the input, with everything stored.
		if n.Content != in[i].Content || n.BatchID == nil || *n.BatchID != batchID ||
			n.CorrelationID == nil || *n.CorrelationID != "corr-batch" || n.Status != domain.StatusPending {
			t.Errorf("created[%d] = %+v, want input %d stored as pending in the batch", i, n, i)
		}
		got, err := repo.GetByID(t.Context(), n.ID)
		if err != nil || !reflect.DeepEqual(got, n) {
			t.Errorf("GetByID(created[%d]) = %+v, %v", i, got, err)
		}
	}
}

// IMP-8: 1000 rows go in as one statement.
func TestCreateBatch_MaxSize(t *testing.T) {
	batchID := uuid.New()
	if _, err := NewNotifications(testDB).CreateBatch(t.Context(), batchOf(domain.MaxBatchSize, batchID)); err != nil {
		t.Fatalf("CreateBatch: %v", err)
	}
	if n := countBatch(t, batchID); n != domain.MaxBatchSize {
		t.Errorf("rows in batch = %d, want %d", n, domain.MaxBatchSize)
	}
}

// One bad row fails the single statement, so no row of the batch is saved.
func TestCreateBatch_AllOrNothing(t *testing.T) {
	batchID := uuid.New()
	ns := batchOf(5, batchID)
	ns[3].Channel = "fax" // bypasses Go validation; the CHECK constraint rejects it

	if _, err := NewNotifications(testDB).CreateBatch(t.Context(), ns); err == nil {
		t.Fatal("CreateBatch succeeded, want a constraint error")
	}
	if n := countBatch(t, batchID); n != 0 {
		t.Errorf("rows in batch = %d, want 0", n)
	}
}

// batchWithKeys gives the items of a batch the keys <key>#0, <key>#1, ...
func batchWithKeys(size int, key string) []domain.Notification {
	ns := batchOf(size, uuid.New())
	for i := range ns {
		ns[i].IdempotencyKey = ptr(fmt.Sprintf("%s#%d", key, i))
	}
	return ns
}

func countKeyPrefix(t *testing.T, key string) int {
	t.Helper()
	var n int
	if err := testDB.QueryRow(t.Context(),
		`SELECT count(*) FROM notifications WHERE idempotency_key LIKE $1`, key+"#%").Scan(&n); err != nil {
		t.Fatalf("count keys: %v", err)
	}
	return n
}

func TestCreateBatch_IdempotencyKeys(t *testing.T) {
	repo := NewNotifications(testDB)
	key := "batch-" + uuid.NewString()

	first, err := repo.CreateBatch(t.Context(), batchWithKeys(3, key))
	if err != nil {
		t.Fatalf("first CreateBatch: %v", err)
	}
	if _, err := repo.CreateBatch(t.Context(), batchWithKeys(3, key)); !errors.Is(err, domain.ErrIdempotencyKeyExists) {
		t.Fatalf("repeat err = %v, want domain.ErrIdempotencyKeyExists", err)
	}

	keys := []string{key + "#0", key + "#1", key + "#2"}
	got, err := repo.GetByIdempotencyKeys(t.Context(), keys)
	if err != nil {
		t.Fatalf("GetByIdempotencyKeys: %v", err)
	}
	if !reflect.DeepEqual(got, first) {
		t.Errorf("GetByIdempotencyKeys returned\n %+v\nwant\n %+v", got, first)
	}
	if n := countKeyPrefix(t, key); n != 3 {
		t.Errorf("rows with the key = %d, want 3", n)
	}
}

// A 5-item batch reusing a 3-item batch's key would insert items 3 and 4; the rollback stops that.
func TestCreateBatch_PartlyTakenKeysSaveNothing(t *testing.T) {
	repo := NewNotifications(testDB)
	key := "batch-" + uuid.NewString()
	if _, err := repo.CreateBatch(t.Context(), batchWithKeys(3, key)); err != nil {
		t.Fatalf("first CreateBatch: %v", err)
	}

	if _, err := repo.CreateBatch(t.Context(), batchWithKeys(5, key)); !errors.Is(err, domain.ErrIdempotencyKeyExists) {
		t.Fatalf("err = %v, want domain.ErrIdempotencyKeyExists", err)
	}
	if n := countKeyPrefix(t, key); n != 3 {
		t.Errorf("rows with the key = %d, want 3 (nothing of the second batch kept)", n)
	}
}

func TestCreateBatch_ConcurrentSameKey(t *testing.T) {
	repo := NewNotifications(testDB)
	key := "batch-" + uuid.NewString()

	const requests, size = 10, 50
	var inserted, conflicts atomic.Int32
	var wg sync.WaitGroup
	for range requests {
		wg.Go(func() {
			_, err := repo.CreateBatch(t.Context(), batchWithKeys(size, key))
			switch {
			case err == nil:
				inserted.Add(1)
			case errors.Is(err, domain.ErrIdempotencyKeyExists):
				conflicts.Add(1)
			default:
				t.Errorf("CreateBatch: %v", err)
			}
		})
	}
	wg.Wait()

	if inserted.Load() != 1 || conflicts.Load() != requests-1 {
		t.Errorf("inserted = %d, conflicts = %d; want 1 and %d", inserted.Load(), conflicts.Load(), requests-1)
	}
	if n := countKeyPrefix(t, key); n != size {
		t.Errorf("rows with the key = %d, want %d", n, size)
	}
}

func TestBatchSummary(t *testing.T) {
	repo := NewNotifications(testDB)
	batchID := uuid.New()
	created, err := repo.CreateBatch(t.Context(), batchOf(4, batchID))
	if err != nil {
		t.Fatalf("CreateBatch: %v", err)
	}
	// The worker doesn't exist yet, so move two rows on by hand.
	if _, err := testDB.Exec(t.Context(), `UPDATE notifications SET status = 'sent' WHERE id = $1`, created[0].ID); err != nil {
		t.Fatalf("update: %v", err)
	}
	if _, err := testDB.Exec(t.Context(), `UPDATE notifications SET status = 'failed' WHERE id = $1`, created[1].ID); err != nil {
		t.Fatalf("update: %v", err)
	}

	got, err := repo.BatchSummary(t.Context(), batchID)
	if err != nil {
		t.Fatalf("BatchSummary: %v", err)
	}
	want := map[domain.Status]int{
		domain.StatusPending: 2, domain.StatusProcessing: 0, domain.StatusSent: 1,
		domain.StatusFailed: 1, domain.StatusCancelled: 0,
	}
	if got.ID != batchID || got.Total != 4 || !reflect.DeepEqual(got.Counts, want) {
		t.Errorf("summary = %+v, want batch %s, total 4, counts %v", got, batchID, want)
	}
	if !got.CreatedAt.Equal(created[0].CreatedAt) {
		t.Errorf("CreatedAt = %v, want %v", got.CreatedAt, created[0].CreatedAt)
	}
}

func TestBatchSummaryNotFound(t *testing.T) {
	_, err := NewNotifications(testDB).BatchSummary(t.Context(), uuid.New())
	if !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("err = %v, want domain.ErrNotFound", err)
	}
}

// insertAt stores a notification with a chosen creation time, so tests control the order.
func insertAt(t *testing.T, batchID uuid.UUID, createdAt time.Time, status domain.Status, channel domain.Channel) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	err := testDB.QueryRow(t.Context(), `
		INSERT INTO notifications (batch_id, channel, recipient, content, priority, status, created_at)
		VALUES ($1, $2, 'r', 'c', 2, $3, $4) RETURNING id`,
		batchID, channel, status, createdAt).Scan(&id)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	return id
}

// listAll walks every page and returns the IDs in order. The batch filter keeps other tests' rows out.
func listAll(t *testing.T, f domain.ListFilter) (ids []uuid.UUID, pages int) {
	t.Helper()
	repo := NewNotifications(testDB)
	for {
		page, err := repo.List(t.Context(), f)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		pages++
		for _, n := range page.Items {
			ids = append(ids, n.ID)
		}
		if page.Next == nil {
			return ids, pages
		}
		f.After = page.Next
	}
}

func TestList_PagesNewestFirst(t *testing.T) {
	batchID := uuid.New()
	base := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	var want []uuid.UUID
	for i := range 7 { // oldest first
		want = append(want, insertAt(t, batchID, base.Add(time.Duration(i)*time.Minute), domain.StatusPending, domain.ChannelSMS))
	}
	slices.Reverse(want) // the list is newest first

	got, pages := listAll(t, domain.ListFilter{BatchID: &batchID, Limit: 3})
	if !slices.Equal(got, want) {
		t.Errorf("ids = %v\nwant  %v", got, want)
	}
	if pages != 3 { // 3 + 3 + 1
		t.Errorf("pages = %d, want 3", pages)
	}
}

// Rows with the same created_at are ordered by id, and paging across them loses nothing.
func TestList_TiesOnCreatedAt(t *testing.T) {
	batchID := uuid.New()
	same := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	inserted := map[uuid.UUID]bool{}
	for range 5 {
		inserted[insertAt(t, batchID, same, domain.StatusPending, domain.ChannelSMS)] = true
	}

	got, _ := listAll(t, domain.ListFilter{BatchID: &batchID, Limit: 2})
	if len(got) != 5 {
		t.Fatalf("got %d rows, want 5", len(got))
	}
	seen := map[uuid.UUID]bool{}
	for i, id := range got {
		if !inserted[id] || seen[id] {
			t.Errorf("row %d (%s) is unknown or a duplicate", i, id)
		}
		seen[id] = true
		if i > 0 && id.String() > got[i-1].String() {
			t.Errorf("row %d is not in descending id order", i)
		}
	}
}

// New rows arriving while a client pages are newer than its cursor, so they never shift later pages.
func TestList_StableWhileRowsArrive(t *testing.T) {
	batchID := uuid.New()
	base := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	for i := range 4 {
		insertAt(t, batchID, base.Add(time.Duration(i)*time.Minute), domain.StatusPending, domain.ChannelSMS)
	}
	repo := NewNotifications(testDB)
	first, err := repo.List(t.Context(), domain.ListFilter{BatchID: &batchID, Limit: 2})
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	insertAt(t, batchID, base.Add(time.Hour), domain.StatusPending, domain.ChannelSMS) // arrives mid-paging

	second, err := repo.List(t.Context(), domain.ListFilter{BatchID: &batchID, Limit: 2, After: first.Next})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(second.Items) != 2 || second.Next != nil {
		t.Fatalf("second page has %d items (next %v), want the 2 older rows and no next page", len(second.Items), second.Next)
	}
	for _, n := range second.Items {
		if n.ID == first.Items[0].ID || n.ID == first.Items[1].ID {
			t.Errorf("row %s appeared on both pages", n.ID)
		}
	}
}

func TestList_Filters(t *testing.T) {
	batchID := uuid.New()
	base := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	failedSMS := insertAt(t, batchID, base, domain.StatusFailed, domain.ChannelSMS)
	sentSMS := insertAt(t, batchID, base.Add(time.Minute), domain.StatusSent, domain.ChannelSMS)
	failedEmail := insertAt(t, batchID, base.Add(2*time.Minute), domain.StatusFailed, domain.ChannelEmail)
	from, to := base.Add(30*time.Second), base.Add(90*time.Second)

	tests := []struct {
		name   string
		filter domain.ListFilter
		want   []uuid.UUID
	}{
		{"status", domain.ListFilter{Status: ptr(domain.StatusFailed)}, []uuid.UUID{failedEmail, failedSMS}},
		{"channel", domain.ListFilter{Channel: ptr(domain.ChannelSMS)}, []uuid.UUID{sentSMS, failedSMS}},
		{"status and channel", domain.ListFilter{Status: ptr(domain.StatusFailed), Channel: ptr(domain.ChannelSMS)},
			[]uuid.UUID{failedSMS}},
		{"from inclusive", domain.ListFilter{From: &base}, []uuid.UUID{failedEmail, sentSMS, failedSMS}},
		{"to exclusive", domain.ListFilter{To: ptr(base.Add(time.Minute))}, []uuid.UUID{failedSMS}},
		{"date range", domain.ListFilter{From: &from, To: &to}, []uuid.UUID{sentSMS}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.filter.BatchID, tt.filter.Limit = &batchID, 10
			got, _ := listAll(t, tt.filter)
			if !slices.Equal(got, tt.want) {
				t.Errorf("ids = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCancelPending(t *testing.T) {
	repo := NewNotifications(testDB)
	batchID := uuid.New()
	base := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)

	pending := insertAt(t, batchID, base, domain.StatusPending, domain.ChannelSMS)
	before, _ := repo.GetByID(t.Context(), pending)
	n, ok, err := repo.CancelPending(t.Context(), pending)
	if err != nil || !ok || n.Status != domain.StatusCancelled || !n.UpdatedAt.After(before.UpdatedAt) {
		t.Errorf("pending: got %+v, ok %v, err %v; want cancelled with a newer updated_at", n, ok, err)
	}

	// Anything not pending, including an already cancelled row, is left alone.
	for _, status := range []domain.Status{domain.StatusProcessing, domain.StatusSent, domain.StatusFailed, domain.StatusCancelled} {
		id := insertAt(t, batchID, base, status, domain.ChannelSMS)
		if _, ok, err := repo.CancelPending(t.Context(), id); err != nil || ok {
			t.Errorf("%s: ok %v, err %v; want not cancelled", status, ok, err)
		}
		if got, _ := repo.GetByID(t.Context(), id); got.Status != status {
			t.Errorf("%s: status changed to %s", status, got.Status)
		}
	}

	if _, ok, err := repo.CancelPending(t.Context(), uuid.New()); err != nil || ok {
		t.Errorf("unknown id: ok %v, err %v; want not cancelled, no error", ok, err)
	}
}

// A cancel and a worker claim racing for the same row: exactly one wins, every time (IMP-4).
func TestCancelPending_RacesWorkerClaim(t *testing.T) {
	repo := NewNotifications(testDB)
	batchID := uuid.New()

	for i := range 200 {
		id := insertAt(t, batchID, time.Now(), domain.StatusPending, domain.ChannelSMS)

		var cancelled, claimed bool
		var cancelErr, claimErr error
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Go(func() {
			<-start
			_, cancelled, cancelErr = repo.CancelPending(t.Context(), id)
		})
		wg.Go(func() {
			<-start
			// The claim the worker will make in Phase 6, reduced to its condition.
			tag, err := testDB.Exec(t.Context(),
				`UPDATE notifications SET status = 'processing' WHERE id = $1 AND status = 'pending'`, id)
			claimed, claimErr = tag.RowsAffected() == 1, err
		})
		close(start)
		wg.Wait()

		if cancelErr != nil || claimErr != nil {
			t.Fatalf("round %d: cancel err %v, claim err %v", i, cancelErr, claimErr)
		}
		if cancelled == claimed {
			t.Fatalf("round %d: cancelled %v, claimed %v; want exactly one", i, cancelled, claimed)
		}
	}
}

func TestCancelPendingInBatch(t *testing.T) {
	repo := NewNotifications(testDB)
	batchID := uuid.New()
	now := time.Now()
	for _, status := range []domain.Status{
		domain.StatusPending, domain.StatusPending, domain.StatusPending, domain.StatusSent, domain.StatusProcessing,
	} {
		insertAt(t, batchID, now, status, domain.ChannelSMS)
	}
	other := insertAt(t, uuid.New(), now, domain.StatusPending, domain.ChannelSMS) // another batch

	n, err := repo.CancelPendingInBatch(t.Context(), batchID)
	if err != nil || n != 3 {
		t.Fatalf("CancelPendingInBatch = %d, %v; want 3", n, err)
	}
	summary, _ := repo.BatchSummary(t.Context(), batchID)
	if c := summary.Counts; c[domain.StatusCancelled] != 3 || c[domain.StatusSent] != 1 || c[domain.StatusProcessing] != 1 {
		t.Errorf("counts after cancel = %v, want 3 cancelled, sent and processing untouched", c)
	}
	if got, _ := repo.GetByID(t.Context(), other); got.Status != domain.StatusPending {
		t.Errorf("other batch's notification is %s, want pending", got.Status)
	}

	// Again: nothing pending is left.
	if n, err := repo.CancelPendingInBatch(t.Context(), batchID); err != nil || n != 0 {
		t.Errorf("second cancel = %d, %v; want 0", n, err)
	}
}

// A batch cancel racing claims on each of its rows: every row ends up exactly one way.
func TestCancelPendingInBatch_RacesWorkerClaims(t *testing.T) {
	repo := NewNotifications(testDB)
	const size = 100
	batchID := uuid.New()
	ids := make([]uuid.UUID, size)
	for i := range ids {
		ids[i] = insertAt(t, batchID, time.Now(), domain.StatusPending, domain.ChannelSMS)
	}

	var cancelled int
	var claimed atomic.Int64
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		<-start
		var err error
		if cancelled, err = repo.CancelPendingInBatch(t.Context(), batchID); err != nil {
			t.Errorf("cancel: %v", err)
		}
	})
	for _, id := range ids {
		wg.Go(func() {
			<-start
			tag, err := testDB.Exec(t.Context(),
				`UPDATE notifications SET status = 'processing' WHERE id = $1 AND status = 'pending'`, id)
			if err != nil {
				t.Errorf("claim: %v", err)
			}
			claimed.Add(tag.RowsAffected())
		})
	}
	close(start)
	wg.Wait()

	summary, _ := repo.BatchSummary(t.Context(), batchID)
	c := summary.Counts
	if cancelled+int(claimed.Load()) != size || c[domain.StatusCancelled] != cancelled || c[domain.StatusProcessing] != int(claimed.Load()) {
		t.Errorf("cancelled %d, claimed %d, counts %v; want them to add up to %d with no overlap",
			cancelled, claimed.Load(), c, size)
	}
}

// clearQueue takes earlier tests' pending rows out of the queue, since claims see the whole table.
func clearQueue(t *testing.T) {
	t.Helper()
	if _, err := testDB.Exec(t.Context(), `UPDATE notifications SET status = 'cancelled' WHERE status = 'pending'`); err != nil {
		t.Fatalf("clear queue: %v", err)
	}
}

// insertQueued stores an SMS notification with a chosen priority, due time and status.
func insertQueued(t *testing.T, p domain.Priority, nextAttemptAt time.Time, status domain.Status) uuid.UUID {
	t.Helper()
	return insertQueuedOn(t, domain.ChannelSMS, p, nextAttemptAt, status)
}

// insertQueuedOn is insertQueued for a chosen channel.
func insertQueuedOn(t *testing.T, ch domain.Channel, p domain.Priority, nextAttemptAt time.Time, status domain.Status) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	err := testDB.QueryRow(t.Context(), `
		INSERT INTO notifications (channel, recipient, content, priority, status, next_attempt_at)
		VALUES ($1, 'r', 'c', $2, $3, $4) RETURNING id`, ch, p, status, nextAttemptAt).Scan(&id)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	return id
}

func TestClaimNext(t *testing.T) {
	clearQueue(t)
	repo := NewNotifications(testDB)
	now := time.Now()

	older := insertQueued(t, domain.PriorityHigh, now.Add(-2*time.Minute), domain.StatusPending)
	newer := insertQueued(t, domain.PriorityHigh, now.Add(-time.Minute), domain.StatusPending)
	insertQueued(t, domain.PriorityHigh, now.Add(time.Hour), domain.StatusPending) // not due yet
	insertQueued(t, domain.PriorityHigh, now.Add(-time.Hour), domain.StatusSent)   // not pending
	normal := insertQueued(t, domain.PriorityNormal, now.Add(-time.Hour), domain.StatusPending)

	// High: oldest due first; the future and the sent rows are never taken.
	for _, want := range []uuid.UUID{older, newer} {
		n, ok, err := repo.ClaimNext(t.Context(), domain.ChannelSMS, domain.PriorityHigh, time.Minute)
		if err != nil || !ok || n.ID != want {
			t.Fatalf("claim = %s, %v, %v; want %s", n.ID, ok, err, want)
		}
		if n.Status != domain.StatusProcessing || n.Attempts != 1 {
			t.Errorf("claimed row: status %s, attempts %d; want processing, 1", n.Status, n.Attempts)
		}
	}
	if _, ok, err := repo.ClaimNext(t.Context(), domain.ChannelSMS, domain.PriorityHigh, time.Minute); err != nil || ok {
		t.Errorf("third high claim: ok %v, err %v; want nothing due", ok, err)
	}

	// Each priority is its own queue.
	if n, ok, _ := repo.ClaimNext(t.Context(), domain.ChannelSMS, domain.PriorityNormal, time.Minute); !ok || n.ID != normal {
		t.Errorf("normal claim = %s, %v; want %s", n.ID, ok, normal)
	}

	// The lease runs from the claim.
	var leaseLeft time.Duration
	if err := testDB.QueryRow(t.Context(),
		`SELECT EXTRACT(EPOCH FROM locked_until - now()) * interval '1 second' FROM notifications WHERE id = $1`,
		older).Scan(&leaseLeft); err != nil {
		t.Fatalf("read lease: %v", err)
	}
	if leaseLeft < 50*time.Second || leaseLeft > time.Minute {
		t.Errorf("lease left = %v, want just under 1m", leaseLeft)
	}
}

// A claim only takes its own channel, so each channel is its own queue (D16).
func TestClaimNext_OnlyItsChannel(t *testing.T) {
	clearQueue(t)
	repo := NewNotifications(testDB)
	due := time.Now().Add(-time.Minute)

	email := insertQueuedOn(t, domain.ChannelEmail, domain.PriorityHigh, due, domain.StatusPending)
	push := insertQueuedOn(t, domain.ChannelPush, domain.PriorityHigh, due.Add(time.Second), domain.StatusPending)

	// Only email and push are due: an SMS claim finds nothing.
	if n, ok, err := repo.ClaimNext(t.Context(), domain.ChannelSMS, domain.PriorityHigh, time.Minute); err != nil || ok {
		t.Fatalf("sms claim = %s, %v, %v; want nothing due", n.ID, ok, err)
	}
	for ch, want := range map[domain.Channel]uuid.UUID{domain.ChannelEmail: email, domain.ChannelPush: push} {
		n, ok, err := repo.ClaimNext(t.Context(), ch, domain.PriorityHigh, time.Minute)
		if err != nil || !ok || n.ID != want || n.Channel != ch {
			t.Errorf("%s claim = %s (%s), %v, %v; want %s", ch, n.ID, n.Channel, ok, err, want)
		}
	}
}

// The claim query uses the channel queue index, not a scan of the whole table.
func TestClaimNext_UsesChannelQueueIndex(t *testing.T) {
	tx, err := testDB.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(t.Context()) }() // only reads; nothing to keep
	// Tiny test tables would make a full scan look cheapest, so take it off the table.
	if _, err := tx.Exec(t.Context(), `SET LOCAL enable_seqscan = off`); err != nil {
		t.Fatal(err)
	}
	rows, err := tx.Query(t.Context(), `
		EXPLAIN SELECT id FROM notifications
		WHERE status = 'pending' AND channel = 'sms' AND priority = 1 AND next_attempt_at <= now()
		ORDER BY next_attempt_at LIMIT 1 FOR UPDATE SKIP LOCKED`)
	if err != nil {
		t.Fatal(err)
	}
	var plan strings.Builder
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		plan.WriteString(line + "\n")
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan.String(), "notifications_channel_queue_idx") {
		t.Errorf("claim plan doesn't use notifications_channel_queue_idx:\n%s", plan.String())
	}
}

// Many workers claiming at once never get the same notification (IMP-2).
func TestClaimNext_ConcurrentClaimsNeverOverlap(t *testing.T) {
	clearQueue(t)
	repo := NewNotifications(testDB)
	const size, workers = 100, 20
	for range size {
		insertQueued(t, domain.PriorityNormal, time.Now().Add(-time.Second), domain.StatusPending)
	}

	var mu sync.Mutex
	claims := map[uuid.UUID]int{}
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			<-start
			for {
				n, ok, err := repo.ClaimNext(t.Context(), domain.ChannelSMS, domain.PriorityNormal, time.Minute)
				if err != nil {
					t.Errorf("claim: %v", err)
					return
				}
				if !ok {
					return
				}
				mu.Lock()
				claims[n.ID]++
				mu.Unlock()
			}
		})
	}
	close(start)
	wg.Wait()

	if len(claims) != size {
		t.Errorf("%d different notifications claimed, want %d", len(claims), size)
	}
	for id, times := range claims {
		if times != 1 {
			t.Errorf("notification %s claimed %d times", id, times)
		}
	}
}

// claimOne claims the only due notification after clearing the queue.
func claimOne(t *testing.T) domain.Notification {
	t.Helper()
	clearQueue(t)
	insertQueued(t, domain.PriorityNormal, time.Now().Add(-time.Second), domain.StatusPending)
	n, ok, err := NewNotifications(testDB).ClaimNext(t.Context(), domain.ChannelSMS, domain.PriorityNormal, time.Minute)
	if err != nil || !ok {
		t.Fatalf("claim: %v, %v", ok, err)
	}
	return n
}

// leaseOf reads locked_until, which isn't part of domain.Notification.
func leaseOf(t *testing.T, id uuid.UUID) *time.Time {
	t.Helper()
	var lease *time.Time
	if err := testDB.QueryRow(t.Context(), `SELECT locked_until FROM notifications WHERE id = $1`, id).Scan(&lease); err != nil {
		t.Fatalf("read lease: %v", err)
	}
	return lease
}

func TestRecordResults(t *testing.T) {
	repo := NewNotifications(testDB)

	t.Run("sent", func(t *testing.T) {
		n := claimOne(t)
		if ok, err := repo.MarkSent(t.Context(), n.ID, n.Attempts, "msg-1"); err != nil || !ok {
			t.Fatalf("MarkSent = %v, %v", ok, err)
		}
		got, _ := repo.GetByID(t.Context(), n.ID)
		if got.Status != domain.StatusSent || got.ProviderMessageID == nil || *got.ProviderMessageID != "msg-1" ||
			got.LastError != nil || leaseOf(t, n.ID) != nil {
			t.Errorf("after MarkSent: %+v; want sent, msg-1, no error, no lease", got)
		}
	})

	t.Run("failed", func(t *testing.T) {
		n := claimOne(t)
		if ok, err := repo.MarkFailed(t.Context(), n.ID, n.Attempts, "provider: status 503"); err != nil || !ok {
			t.Fatalf("MarkFailed = %v, %v", ok, err)
		}
		got, _ := repo.GetByID(t.Context(), n.ID)
		if got.Status != domain.StatusFailed || got.LastError == nil || *got.LastError != "provider: status 503" ||
			leaseOf(t, n.ID) != nil {
			t.Errorf("after MarkFailed: %+v; want failed with the error, no lease", got)
		}
	})

	t.Run("released", func(t *testing.T) {
		n := claimOne(t)
		if ok, err := repo.Release(t.Context(), n.ID, n.Attempts); err != nil || !ok {
			t.Fatalf("Release = %v, %v", ok, err)
		}
		// It is due again: the next claim gets it.
		again, ok, err := repo.ClaimNext(t.Context(), domain.ChannelSMS, domain.PriorityNormal, time.Minute)
		if err != nil || !ok || again.ID != n.ID || again.Attempts != 2 {
			t.Errorf("claim after release = %s (attempt %d), %v, %v; want the same notification, attempt 2",
				again.ID, again.Attempts, ok, err)
		}
	})

	// A claim that was superseded can't overwrite the newer claim's result.
	t.Run("stale attempt changes nothing", func(t *testing.T) {
		n := claimOne(t)
		if ok, err := repo.MarkSent(t.Context(), n.ID, n.Attempts+1, "msg-x"); err != nil || ok {
			t.Errorf("MarkSent with another attempt = %v, %v; want not recorded", ok, err)
		}
		if got, _ := repo.GetByID(t.Context(), n.ID); got.Status != domain.StatusProcessing {
			t.Errorf("status = %s, want still processing", got.Status)
		}
	})

	t.Run("not processing changes nothing", func(t *testing.T) {
		n := claimOne(t)
		if _, err := repo.MarkSent(t.Context(), n.ID, n.Attempts, "msg-1"); err != nil {
			t.Fatal(err)
		}
		if ok, err := repo.MarkFailed(t.Context(), n.ID, n.Attempts, "late"); err != nil || ok {
			t.Errorf("MarkFailed on a sent notification = %v, %v; want not recorded", ok, err)
		}
	})
}
