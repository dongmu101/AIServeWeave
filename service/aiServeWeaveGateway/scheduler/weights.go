package scheduler

type targetCandidates struct {
	priority   int
	weight     int
	candidates []Candidate
}

// weightedCandidates permutes available targets within each sorted priority group.
// Each draw removes one target, preserving its candidate load order and all fallbacks.
// weightedCandidates 在每个已排序优先级分组内排列可用目标，每次抽取移除一个目标，
// 保留目标内部负载顺序和全部回退候选。
func weightedCandidates(groups []targetCandidates, draw func() float64) []Candidate {
	available := groups[:0]
	for _, group := range groups {
		if len(group.candidates) > 0 {
			available = append(available, group)
		}
	}
	groups = available
	for start := 0; start < len(groups); {
		end := start + 1
		for end < len(groups) && groups[end].priority == groups[start].priority {
			end++
		}
		for i := start; i < end-1; i++ {
			total := float64(0)
			for j := i; j < end; j++ {
				total += float64(max(groups[j].weight, 1))
			}
			remaining := draw() * total
			selected := end - 1
			for j := i; j < end; j++ {
				remaining -= float64(max(groups[j].weight, 1))
				if remaining < 0 {
					selected = j
					break
				}
			}
			groups[i], groups[selected] = groups[selected], groups[i]
		}
		start = end
	}

	// One target is the common case, and its candidates are already distinct
	// (one per node and runtime), so there is nothing to de-duplicate and no
	// reason to copy them: pick hands over a slice nothing else holds.
	//
	// 只有一个 target 是常见情形，其候选本就互不相同（每个节点与运行时一个），无需去重，
	// 也没有理由复制它们：pick 交出的切片没有别人持有。
	switch len(groups) {
	case 0:
		return nil
	case 1:
		return groups[0].candidates
	}

	total := 0
	for _, group := range groups {
		total += len(group.candidates)
	}
	out := make([]Candidate, 0, total)
	// Every candidate of one group carries that target's runtime model, and a
	// group's own candidates are distinct, so two groups can only share a
	// candidate when they share a model. With all models different — the usual
	// alias with a primary and a fallback model — nothing needs de-duplicating.
	//
	// 同一组的每个候选都带着该 target 的运行时模型，且组内候选本就互不相同，因此只有当
	// 两组共用同一个模型时才可能出现相同候选。所有模型都不同时——常见的「主模型加回退
	// 模型」别名——就无需去重。
	if distinctModels(groups) {
		for _, group := range groups {
			out = append(out, group.candidates...)
		}
		return out
	}
	seen := make(map[Candidate]struct{}, total)
	for _, group := range groups {
		for _, c := range group.candidates {
			if _, dup := seen[c]; dup {
				continue
			}
			seen[c] = struct{}{}
			out = append(out, c)
		}
	}
	return out
}

// distinctModels reports whether no two groups serve the same runtime model.
// Groups passed in are never empty.
//
// distinctModels 报告是否没有任何两组服务同一个运行时模型。传入的分组都不为空。
func distinctModels(groups []targetCandidates) bool {
	for i := 1; i < len(groups); i++ {
		for j := 0; j < i; j++ {
			if groups[i].candidates[0].Model == groups[j].candidates[0].Model {
				return false
			}
		}
	}
	return true
}
