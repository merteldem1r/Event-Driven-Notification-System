package domain

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestTemplateValidate(t *testing.T) {
	tests := []struct {
		name       string
		tmpl       Template
		wantFields []string // nil means valid
	}{
		{"valid", Template{Name: "order_shipped", Body: "Hi {{first_name}}, order {{order_id}} has shipped."}, nil},
		{"no placeholders", Template{Name: "welcome", Body: "Welcome!"}, nil},
		{"spaces inside braces", Template{Name: "otp-code", Body: "Your code is {{ code }}"}, nil},
		{"dots and digits in name", Template{Name: "welcome.v2", Body: "Hi"}, nil},
		{"name at max length", Template{Name: strings.Repeat("a", MaxTemplateNameLength), Body: "Hi"}, nil},

		{"empty name", Template{Name: "", Body: "Hi"}, []string{"name"}},
		{"uppercase name", Template{Name: "OrderShipped", Body: "Hi"}, []string{"name"}},
		{"name with space", Template{Name: "order shipped", Body: "Hi"}, []string{"name"}},
		{"name starting with _", Template{Name: "_order", Body: "Hi"}, []string{"name"}},
		{"name too long", Template{Name: strings.Repeat("a", MaxTemplateNameLength+1), Body: "Hi"}, []string{"name"}},

		{"blank body", Template{Name: "a", Body: " \n "}, []string{"body"}},
		{"body too long", Template{Name: "a", Body: strings.Repeat("a", MaxTemplateBodyLength+1)}, []string{"body"}},
		{"unclosed placeholder", Template{Name: "a", Body: "Hi {{first_name"}, []string{"body"}},
		{"placeholder with space in name", Template{Name: "a", Body: "Hi {{first name}}"}, []string{"body"}},
		{"stray closing braces", Template{Name: "a", Body: "Hi }}"}, []string{"body"}},

		{"both wrong", Template{Name: "", Body: ""}, []string{"name", "body"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.tmpl.Validate()
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

func TestTemplateVariables(t *testing.T) {
	tmpl := Template{Body: "{{b}} then {{ a }}, {{b}} again"}
	if got := tmpl.Variables(); !slices.Equal(got, []string{"b", "a"}) {
		t.Errorf("Variables() = %v, want [b a]: each once, in order of first use", got)
	}
	if got := (Template{Body: "no placeholders"}).Variables(); got != nil {
		t.Errorf("Variables() = %v, want none", got)
	}
}

func TestTemplateRender(t *testing.T) {
	tmpl := Template{Name: "order_shipped", Body: "Hi {{first_name}}, order {{ order_id }} has shipped. Thanks, {{first_name}}!"}

	tests := []struct {
		name        string
		vars        map[string]string
		want        string
		wantMissing []string // nil means rendered
	}{
		{"all variables", map[string]string{"first_name": "Ayse", "order_id": "1042"},
			"Hi Ayse, order 1042 has shipped. Thanks, Ayse!", nil},
		{"extra variables are ignored", map[string]string{"first_name": "Ayse", "order_id": "1042", "coupon": "X"},
			"Hi Ayse, order 1042 has shipped. Thanks, Ayse!", nil},
		{"empty value is allowed", map[string]string{"first_name": "", "order_id": "1042"},
			"Hi , order 1042 has shipped. Thanks, !", nil},
		{"values are not filled in again", map[string]string{"first_name": "{{order_id}}", "order_id": "1042"},
			"Hi {{order_id}}, order 1042 has shipped. Thanks, {{order_id}}!", nil},
		{"one missing", map[string]string{"first_name": "Ayse"}, "", []string{"variables.order_id"}},
		{"all missing, in order", nil, "", []string{"variables.first_name", "variables.order_id"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tmpl.Render(tt.vars)
			if tt.wantMissing == nil {
				if err != nil || got != tt.want {
					t.Fatalf("Render = %q, %v; want %q", got, err, tt.want)
				}
				return
			}
			var ve *ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("Render error = %v, want a *ValidationError", err)
			}
			var fields []string
			for _, f := range ve.Fields {
				fields = append(fields, f.Field)
			}
			if !slices.Equal(fields, tt.wantMissing) {
				t.Errorf("missing = %v, want %v", fields, tt.wantMissing)
			}
		})
	}
}
