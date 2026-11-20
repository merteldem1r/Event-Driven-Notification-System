package service

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/merteldem1r/Event-Driven-Notification-System/internal/domain"
)

// fakeTemplateStore keeps templates in a map.
type fakeTemplateStore struct {
	templates map[string]domain.Template
	calls     int
	getErr    error // returned by Get, e.g. a database failure
}

func (f *fakeTemplateStore) Create(_ context.Context, t domain.Template) (domain.Template, error) {
	f.calls++
	if _, ok := f.templates[t.Name]; ok {
		return domain.Template{}, domain.ErrTemplateExists
	}
	if f.templates == nil {
		f.templates = map[string]domain.Template{}
	}
	f.templates[t.Name] = t
	return t, nil
}

func (f *fakeTemplateStore) Get(_ context.Context, name string) (domain.Template, error) {
	f.calls++
	if f.getErr != nil {
		return domain.Template{}, f.getErr
	}
	t, ok := f.templates[name]
	if !ok {
		return domain.Template{}, domain.ErrNotFound
	}
	return t, nil
}

func (f *fakeTemplateStore) List(context.Context) ([]domain.Template, error) {
	return nil, nil
}

func TestTemplatesCreate(t *testing.T) {
	store := &fakeTemplateStore{}
	svc := NewTemplates(store)

	if _, err := svc.Create(t.Context(), domain.Template{Name: "welcome", Body: "Hi {{name}}"}); err != nil {
		t.Fatalf("valid template: %v", err)
	}

	var ve *domain.ValidationError
	if _, err := svc.Create(t.Context(), domain.Template{Name: "Bad Name", Body: "Hi"}); !errors.As(err, &ve) {
		t.Errorf("invalid template: err = %v, want a *domain.ValidationError", err)
	}
	if store.calls != 1 {
		t.Errorf("store called %d times, want once: an invalid template isn't saved", store.calls)
	}

	if _, err := svc.Create(t.Context(), domain.Template{Name: "welcome", Body: "Hello"}); !errors.Is(err, domain.ErrTemplateExists) {
		t.Errorf("taken name: err = %v, want ErrTemplateExists", err)
	}
}

// orderShipped is the template the notification tests render.
func orderShipped() *fakeTemplateStore {
	return &fakeTemplateStore{templates: map[string]domain.Template{
		"order_shipped": {Name: "order_shipped", Body: "Hi {{first_name}}, order {{order_id}} has shipped."},
	}}
}

// fromTemplate is a valid SMS that uses order_shipped instead of content.
func fromTemplate(vars map[string]string) domain.Notification {
	return domain.Notification{
		Channel: domain.ChannelSMS, Recipient: "+905551234567", Priority: domain.PriorityNormal,
		Template: ptr("order_shipped"), Variables: vars,
	}
}

func TestCreate_FromTemplate(t *testing.T) {
	store := &fakeStore{}
	_, _, err := NewNotifications(store, orderShipped()).
		Create(t.Context(), fromTemplate(map[string]string{"first_name": "Ayse", "order_id": "1042", "extra": "ignored"}))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	saved := store.saved[0]
	if saved.Content != "Hi Ayse, order 1042 has shipped." || saved.Template == nil || *saved.Template != "order_shipped" {
		t.Errorf("saved content %q, template %v; want the rendered text and the template name", saved.Content, saved.Template)
	}
}

func TestCreate_TemplateProblems(t *testing.T) {
	withContent := fromTemplate(map[string]string{"first_name": "A", "order_id": "1"})
	withContent.Content = "hello"
	unknown := fromTemplate(nil)
	unknown.Template = ptr("missing")
	varsOnly := validNotification()
	varsOnly.Variables = map[string]string{"a": "b"}
	tooLong := fromTemplate(map[string]string{"first_name": strings.Repeat("a", domain.MaxSMSLength), "order_id": "1"})

	tests := []struct {
		name       string
		n          domain.Notification
		wantFields []string
	}{
		{"content and template", withContent, []string{"content"}},
		{"unknown template", unknown, []string{"template"}},
		{"missing variables", fromTemplate(map[string]string{"first_name": "A"}), []string{"variables.order_id"}},
		{"variables without template", varsOnly, []string{"variables"}},
		{"rendered text too long for sms", tooLong, []string{"content"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &fakeStore{}
			_, _, err := NewNotifications(store, orderShipped()).Create(t.Context(), tt.n)
			var ve *domain.ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("err = %v, want a *domain.ValidationError", err)
			}
			var got []string
			for _, f := range ve.Fields {
				got = append(got, f.Field)
			}
			if !slices.Equal(got, tt.wantFields) {
				t.Errorf("invalid fields = %v, want %v", got, tt.wantFields)
			}
			if len(store.saved) != 0 {
				t.Error("an invalid notification was saved")
			}
		})
	}
}

// A failed lookup is a server error, not the client's mistake.
func TestCreate_TemplateLookupFails(t *testing.T) {
	templates := &fakeTemplateStore{getErr: errors.New("connection refused")}
	_, _, err := NewNotifications(&fakeStore{}, templates).Create(t.Context(), fromTemplate(nil))
	var ve *domain.ValidationError
	if err == nil || errors.As(err, &ve) {
		t.Fatalf("err = %v, want a plain error", err)
	}
}

// In a batch, problems are named by position and each template is loaded once.
func TestCreateBatch_FromTemplate(t *testing.T) {
	ok := fromTemplate(map[string]string{"first_name": "A", "order_id": "1"})
	unknown := fromTemplate(nil)
	unknown.Template = ptr("missing")

	templates := orderShipped()
	_, _, err := NewNotifications(&fakeStore{}, templates).CreateBatch(t.Context(),
		[]domain.Notification{ok, ok, fromTemplate(nil), unknown, unknown}, nil)
	var ve *domain.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("err = %v, want a *domain.ValidationError", err)
	}
	var got []string
	for _, f := range ve.Fields {
		got = append(got, f.Field)
	}
	want := []string{"notifications[2].variables.first_name", "notifications[2].variables.order_id",
		"notifications[3].template", "notifications[4].template"}
	if !slices.Equal(got, want) {
		t.Errorf("invalid fields = %v, want %v", got, want)
	}
	if templates.calls != 2 {
		t.Errorf("template lookups = %d, want 2: one per distinct name, a missing one included", templates.calls)
	}

	store := &fakeStore{}
	batch, _, err := NewNotifications(store, orderShipped()).CreateBatch(t.Context(), []domain.Notification{ok, validNotification()}, nil)
	if err != nil {
		t.Fatalf("valid batch: %v", err)
	}
	if batch.Notifications[0].Content != "Hi A, order 1 has shipped." || batch.Notifications[1].Content != "hello" {
		t.Errorf("contents = %q, %q", batch.Notifications[0].Content, batch.Notifications[1].Content)
	}
}
