package configui

import "AIServeWeave/common/runtime"

type modelCatalogue struct {
	Live     bool
	Count    int
	Runtimes []runtimeModels
}

type runtimeModels struct {
	ID    string
	Kind  runtime.Kind
	State runtime.State
	IDs   []string
}

// availableModels projects only runtime identities, states and healthy model IDs.
// availableModels 仅提取运行时标识、状态和健康运行时的模型 ID。
func availableModels(snapshots func() []runtime.Snapshot) modelCatalogue {
	view := modelCatalogue{Live: snapshots != nil}
	if snapshots == nil {
		return view
	}
	all := make(map[string]bool)
	for _, snapshot := range snapshots() {
		entry := runtimeModels{ID: snapshot.Descriptor.ID, Kind: snapshot.Descriptor.Kind, State: snapshot.State}
		if snapshot.State == runtime.StateHealthy {
			seen := make(map[string]bool)
			for _, model := range snapshot.Discovery.Models {
				if model.ID == "" || seen[model.ID] {
					continue
				}
				seen[model.ID] = true
				all[model.ID] = true
				entry.IDs = append(entry.IDs, model.ID)
			}
		}
		view.Runtimes = append(view.Runtimes, entry)
	}
	view.Count = len(all)
	return view
}
