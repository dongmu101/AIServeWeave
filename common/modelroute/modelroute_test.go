package modelroute

import "testing"

func TestValidateAndDigest(t *testing.T) {
	for _, tt := range []struct {
		name   string
		routes []Route
		valid  bool
	}{
		{"empty explicit table", []Route{}, true},
		{"valid selector", []Route{{Model: "alias", Targets: []Target{{RuntimeModel: "backend", NodeSelector: map[string]string{"region": "local"}}}}}, true},
		{"empty alias", []Route{{Targets: []Target{{RuntimeModel: "backend"}}}}, false},
		{"no target", []Route{{Model: "alias"}}, false},
		{"negative weight", []Route{{Model: "alias", Targets: []Target{{RuntimeModel: "backend", Weight: -1}}}}, false},
		{"duplicate alias", []Route{{Model: "alias", Targets: []Target{{RuntimeModel: "backend"}}}, {Model: "alias", Targets: []Target{{RuntimeModel: "other"}}}}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := Validate(tt.routes); (err == nil) != tt.valid {
				t.Fatalf("Validate = %v, want valid %v", err, tt.valid)
			}
		})
	}
	a := []Route{{Model: "b", Targets: []Target{{RuntimeModel: "m", NodeSelector: map[string]string{"b": "2", "a": "1"}}}}, {Model: "a", Targets: []Target{{RuntimeModel: "n"}}}}
	b := []Route{a[1], a[0]}
	da, err := Digest(a)
	if err != nil {
		t.Fatal(err)
	}
	db, err := Digest(b)
	if err != nil || da != db {
		t.Fatalf("digest = %s %v, want %s", db, err, da)
	}
	b[0].Targets = []Target{{RuntimeModel: "changed"}}
	dc, _ := Digest(b)
	if dc == da {
		t.Fatal("changed routes retained digest")
	}
}

func TestPublicationBoundsAndSafeNumbers(t *testing.T) {
	for _, tc := range []struct {
		name   string
		target Target
		valid  bool
	}{
		{"largest safe weight", Target{RuntimeModel: "m", Weight: 9007199254740991}, true},
		{"unsafe weight", Target{RuntimeModel: "m", Weight: 9007199254740992}, false},
		{"unsafe priority", Target{RuntimeModel: "m", Priority: -9007199254740992}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := Validate([]Route{{Model: "a", Targets: []Target{tc.target}}})
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v, want %v", err == nil, tc.valid)
			}
		})
	}
}

func TestSelectorRequiresAnExplicitEmptyLabel(t *testing.T) {
	target := Target{NodeSelector: map[string]string{"region": ""}}
	for _, tc := range []struct {
		name   string
		labels map[string]string
		want   bool
	}{
		{"absent", map[string]string{}, false},
		{"explicit empty", map[string]string{"region": ""}, true},
		{"different", map[string]string{"region": "local"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := target.MatchesNode(tc.labels); got != tc.want {
				t.Fatalf("match=%v, want %v", got, tc.want)
			}
		})
	}
}
