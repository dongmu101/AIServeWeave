package installcode

import (
	"regexp"
	"testing"
)

func TestNewIsWellFormedAndUnique(t *testing.T) {
	display := regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{4}(-[0-9A-HJKMNP-TV-Z]{4}){4}$`)
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		code, err := New()
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}
		if !display.MatchString(code) {
			t.Fatalf("New() = %q, want XXXX-XXXX-XXXX-XXXX-XXXX in Crockford Base32", code)
		}
		if seen[code] {
			t.Fatalf("New() returned %q twice", code)
		}
		seen[code] = true
	}
}

func TestNormalizeAndHash(t *testing.T) {
	const canonical = "K7QF9M2DX4TBH8RW3NPC"
	for _, tt := range []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{name: "display form", input: "K7QF-9M2D-X4TB-H8RW-3NPC", want: canonical},
		{name: "lower case", input: "k7qf-9m2d-x4tb-h8rw-3npc", want: canonical},
		{name: "no dashes", input: canonical, want: canonical},
		{name: "surrounding space", input: "  K7QF-9M2D-X4TB-H8RW-3NPC\n", want: canonical},
		{name: "too short", input: "K7QF-9M2D", wantErr: true},
		{name: "too long", input: canonical + "A", wantErr: true},
		{name: "excluded letter U", input: "U7QF-9M2D-X4TB-H8RW-3NPC", wantErr: true},
		{name: "symbol", input: "K7QF-9M2D-X4TB-H8RW-3NP!", wantErr: true},
		{name: "empty", input: "", wantErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Normalize(tt.input)
			if (err != nil) != tt.wantErr || got != tt.want {
				t.Fatalf("Normalize(%q) = %q, %v; want %q, error %v", tt.input, got, err, tt.want, tt.wantErr)
			}
		})
	}
	a, _ := Hash("K7QF-9M2D-X4TB-H8RW-3NPC")
	b, _ := Hash("k7qf9m2dx4tbh8rw3npc")
	if a == "" || a != b || len(a) != 64 {
		t.Fatalf("Hash differs across spellings of one code: %q vs %q", a, b)
	}
	if _, err := Hash("nope"); err == nil {
		t.Fatal("Hash(invalid) error = nil, want ErrInvalid")
	}
}
