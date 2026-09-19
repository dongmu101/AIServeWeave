package comfyuimanagedstatus

import (
	"reflect"
	"testing"
)

func TestReconcileCustomNodes(t *testing.T) {
	tests := []struct {
		name      string
		declared  []DeclaredCustomNode
		installed []CustomNodeStatus
		want      []CustomNodeMismatch
	}{
		{
			name:      "no declared dependencies never mismatch",
			declared:  nil,
			installed: []CustomNodeStatus{{Name: "a", Version: "v1"}},
			want:      nil,
		},
		{
			name:      "declared but not installed is missing",
			declared:  []DeclaredCustomNode{{Name: "a"}},
			installed: nil,
			want:      []CustomNodeMismatch{{Name: "a", Reason: "missing"}},
		},
		{
			name:      "installed with matching pinned version satisfies",
			declared:  []DeclaredCustomNode{{Name: "a", Version: "v1"}},
			installed: []CustomNodeStatus{{Name: "a", Version: "v1"}},
			want:      nil,
		},
		{
			name:      "installed with different pinned version mismatches",
			declared:  []DeclaredCustomNode{{Name: "a", Version: "v1"}},
			installed: []CustomNodeStatus{{Name: "a", Version: "v2"}},
			want:      []CustomNodeMismatch{{Name: "a", Reason: "version_mismatch"}},
		},
		{
			name:      "declared with empty version accepts any installed version",
			declared:  []DeclaredCustomNode{{Name: "a"}},
			installed: []CustomNodeStatus{{Name: "a", Version: "whatever"}},
			want:      nil,
		},
		{
			name: "multiple declared dependencies report each unsatisfied one",
			declared: []DeclaredCustomNode{
				{Name: "a", Version: "v1"},
				{Name: "b"},
				{Name: "c", Version: "v1"},
			},
			installed: []CustomNodeStatus{
				{Name: "a", Version: "v1"},
				{Name: "c", Version: "v2"},
			},
			want: []CustomNodeMismatch{
				{Name: "b", Reason: "missing"},
				{Name: "c", Reason: "version_mismatch"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ReconcileCustomNodes(tt.declared, tt.installed)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ReconcileCustomNodes(%+v, %+v) = %+v, want %+v", tt.declared, tt.installed, got, tt.want)
			}
		})
	}
}
