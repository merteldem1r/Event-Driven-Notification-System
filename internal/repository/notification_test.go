package repository

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
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
