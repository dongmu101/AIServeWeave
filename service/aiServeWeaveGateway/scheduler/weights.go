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

	var out []Candidate
	seen := make(map[Candidate]struct{})
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
