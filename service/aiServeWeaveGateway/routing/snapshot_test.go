package routing

import "testing"

func TestNewCopiesRoutes(t *testing.T) {
	routes := []Route{{Model: "alias", Targets: []Target{{RuntimeModel: "real", NodeSelector: map[string]string{"zone": "a"}}}}}
	table, err := New(routes)
	if err != nil {
		t.Fatal(err)
	}
	routes[0].Targets[0].NodeSelector["zone"] = "b"
	got, _ := table.Resolve("alias")
	if got[0].NodeSelector["zone"] != "a" {
		t.Fatalf("want a, got %q", got[0].NodeSelector["zone"])
	}
	got[0].NodeSelector["zone"] = "c"
	again, _ := table.Resolve("alias")
	if again[0].NodeSelector["zone"] != "a" {
		t.Fatalf("want a, got %q", again[0].NodeSelector["zone"])
	}
}
