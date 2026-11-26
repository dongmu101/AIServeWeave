package flowdef

import (
	"slices"
	"testing"
)

func TestParseTemplate(t *testing.T) {
	values := map[Ref]string{
		{InputsScope, "topic"}: "cats",
		{"draft", "text"}:      "DRAFT",
	}
	for _, tt := range []struct {
		name     string
		src      string
		refs     []Ref
		rendered string
	}{
		{"plain text", "no references here", nil, "no references here"},
		{"input reference", "Write about {{inputs.topic}}.", []Ref{{InputsScope, "topic"}}, "Write about cats."},
		{"spaces inside braces", "{{ inputs.topic }}", []Ref{{InputsScope, "topic"}}, "cats"},
		{"node reference repeated", "{{draft.text}}/{{draft.text}}", []Ref{{"draft", "text"}, {"draft", "text"}}, "DRAFT/DRAFT"},
		{"single name stays literal", "Hello {{name}}", nil, "Hello {{name}}"},
		{"pipe stays literal", "{{ a | b }}", nil, "{{ a | b }}"},
		{"json stays literal", `{{"a":1}}`, nil, `{{"a":1}}`},
		{"leading digit stays literal", "{{1a.b}}", nil, "{{1a.b}}"},
		{"unclosed stays literal", "{{inputs.topic", nil, "{{inputs.topic"},
		{"extra braces keep the reference", "{{{inputs.topic}}}", []Ref{{InputsScope, "topic"}}, "{cats}"},
		{"multibyte text", "主题：{{inputs.topic}}。", []Ref{{InputsScope, "topic"}}, "主题：cats。"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tmpl, err := ParseTemplate(tt.src)
			if err != nil {
				t.Fatalf("ParseTemplate() error = %v", err)
			}
			if got := tmpl.Refs(); !slices.Equal(got, tt.refs) {
				t.Fatalf("Refs() = %v, want %v", got, tt.refs)
			}
			if got := tmpl.Render(func(r Ref) string { return values[r] }); got != tt.rendered {
				t.Fatalf("Render() = %q, want %q", got, tt.rendered)
			}
		})
	}
}

func TestParseTemplateRejectsNestedFields(t *testing.T) {
	if _, err := ParseTemplate("{{draft.data.score}}"); err == nil {
		t.Fatal("ParseTemplate() accepted a nested field reference")
	}
}

func TestIsIdent(t *testing.T) {
	long := make([]byte, MaxIdentLength+1)
	for i := range long {
		long[i] = 'a'
	}
	for s, want := range map[string]bool{
		"a": true, "_x": true, "node_1": true, "Draft2": true,
		"": false, "1a": false, "a-b": false, "a.b": false, "名": false,
		string(long[:MaxIdentLength]): true, string(long): false,
	} {
		if got := isIdent(s); got != want {
			t.Errorf("isIdent(%q) = %v, want %v", s, got, want)
		}
	}
}
