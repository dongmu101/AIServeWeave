package main

import "context"

type clientStats struct {
	First, Second, Calls int
	Order                []string
}

func serveProbeClient(ctx context.Context, b *broker, expected, scenario string) clientStats {
	var stats clientStats
	var first, second *toolCall
	resolve := func(c toolCall, result toolResult, label string) {
		if b.resolve(b.owner, c.ID, result) == nil {
			stats.Calls++
			stats.Order = append(stats.Order, label)
		}
	}
	for {
		select {
		case <-ctx.Done():
			return stats
		case c := <-b.calls:
			valid := false
			switch c.Label {
			case "first":
				if stats.First == 0 && (scenario == "parallel" || stats.Second == 0) {
					stats.First++
					first = &c
					valid = true
				}
			case "second":
				if stats.Second == 0 && (scenario == "parallel" || stats.First == 1) {
					stats.Second++
					second = &c
					valid = true
				}
			}
			if !valid {
				resolve(c, toolResult{Text: "unexpected probe sequence", IsError: true}, "rejected")
				continue
			}
			if scenario == "parallel" {
				if first != nil && second != nil {
					resolve(*second, toolResult{Text: expected}, "second")
					resolve(*first, toolResult{Text: "first call acknowledged"}, "first")
					first, second = nil, nil
				}
			} else if c.Label == "first" {
				resolve(c, toolResult{Text: "first call acknowledged"}, "first")
			} else {
				resolve(c, toolResult{Text: expected}, "second")
			}
		}
	}
}
