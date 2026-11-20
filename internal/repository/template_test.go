package repository

import (
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/merteldem1r/Event-Driven-Notification-System/internal/domain"
)

func TestTemplates_CreateGetList(t *testing.T) {
	repo := NewTemplates(testDB)
	name := "order_shipped_" + uuid.NewString()[:8] // unique per run
	in := domain.Template{Name: name, Body: "Hi {{first_name}}, order {{order_id}} has shipped."}

	created, err := repo.Create(t.Context(), in)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.Name != in.Name || created.Body != in.Body || created.CreatedAt.IsZero() {
		t.Errorf("Create returned %+v", created)
	}

	got, err := repo.Get(t.Context(), name)
	if err != nil || got != created {
		t.Errorf("Get = %+v, %v; want %+v", got, err, created)
	}

	list, err := repo.List(t.Context())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	found := false
	for i, tmpl := range list {
		found = found || tmpl == created
		if i > 0 && list[i-1].Name >= tmpl.Name {
			t.Errorf("List not ordered by name: %q before %q", list[i-1].Name, tmpl.Name)
		}
	}
	if !found {
		t.Error("List doesn't contain the created template")
	}
}

// A name is taken once: the second create changes nothing and says so.
func TestTemplates_CreateDuplicateName(t *testing.T) {
	repo := NewTemplates(testDB)
	name := "dup_" + uuid.NewString()[:8]
	if _, err := repo.Create(t.Context(), domain.Template{Name: name, Body: "first"}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Create(t.Context(), domain.Template{Name: name, Body: "second"}); !errors.Is(err, domain.ErrTemplateExists) {
		t.Fatalf("second Create = %v, want ErrTemplateExists", err)
	}
	if got, _ := repo.Get(t.Context(), name); got.Body != "first" {
		t.Errorf("body = %q after a duplicate create, want the first one kept", got.Body)
	}
}

func TestTemplates_GetNotFound(t *testing.T) {
	if _, err := NewTemplates(testDB).Get(t.Context(), "missing_"+uuid.NewString()[:8]); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("Get = %v, want ErrNotFound", err)
	}
}

// A notification keeps the name of its template; the name must exist.
func TestCreate_WithTemplate(t *testing.T) {
	name := "receipt_" + uuid.NewString()[:8]
	if _, err := NewTemplates(testDB).Create(t.Context(), domain.Template{Name: name, Body: "Paid {{amount}}"}); err != nil {
		t.Fatal(err)
	}
	repo := NewNotifications(testDB)
	n := domain.Notification{
		Channel: domain.ChannelSMS, Recipient: "+905551234567", Content: "Paid 10 TL", Priority: domain.PriorityNormal,
		Template: &name,
	}
	created, err := repo.Create(t.Context(), n)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := repo.GetByID(t.Context(), created.ID)
	if err != nil || got.Template == nil || *got.Template != name {
		t.Errorf("stored template = %v, %v; want %q", got.Template, err, name)
	}

	missing := "missing_" + uuid.NewString()[:8]
	n.Template = &missing
	if _, err := repo.Create(t.Context(), n); err == nil {
		t.Error("Create with an unknown template succeeded, want the foreign key to refuse it")
	}
}
