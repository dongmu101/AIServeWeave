// Command pricecatalog-snapshot trims an upstream LiteLLM price list into the
// control plane's built-in price catalog snapshot, with the same rules the
// control plane parses it by. scripts/update-price-catalog.sh runs it.
//
// pricecatalog-snapshot 命令把上游 LiteLLM 价目表裁剪为控制面内置的价目表快照，所用规则
// 与控制面解析价目表的规则相同。scripts/update-price-catalog.sh 会运行它。
//
// Usage / 用法:
//
//	go run ./service/aiServeWeaveControlPlane/internal/pricecatalog/cmd/pricecatalog-snapshot -in upstream.json -out snapshot.json
package main

import (
	"flag"
	"fmt"
	"os"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/pricecatalog"
)

func main() {
	in := flag.String("in", "", "upstream model_prices_and_context_window.json")
	out := flag.String("out", "", "snapshot to write (stdout when empty)")
	flag.Parse()
	if *in == "" {
		fmt.Fprintln(os.Stderr, "pricecatalog-snapshot: -in is required")
		os.Exit(2)
	}
	data, err := os.ReadFile(*in)
	if err != nil {
		fmt.Fprintln(os.Stderr, "pricecatalog-snapshot:", err)
		os.Exit(1)
	}
	trimmed, err := pricecatalog.Trim(data)
	if err != nil {
		fmt.Fprintln(os.Stderr, "pricecatalog-snapshot:", err)
		os.Exit(1)
	}
	snap, err := pricecatalog.Parse(trimmed)
	if err != nil {
		fmt.Fprintln(os.Stderr, "pricecatalog-snapshot: the trimmed snapshot does not parse:", err)
		os.Exit(1)
	}
	if *out == "" {
		_, err = os.Stdout.Write(trimmed)
	} else {
		err = os.WriteFile(*out, trimmed, 0o644)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "pricecatalog-snapshot:", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "pricecatalog-snapshot: %d entries\n", len(snap.Entries))
}
