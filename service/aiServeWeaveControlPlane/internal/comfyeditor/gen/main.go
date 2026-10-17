// Command gen regenerates the editor's compiled-in core node catalogue from a
// raw ComfyUI /object_info export:
//
//	go run ./service/aiServeWeaveControlPlane/internal/comfyeditor/gen \
//	  -in object_info.json -comfyui 0.41.2 -frontend 1.55.17
//
// The export may come from an instance with custom nodes installed; only core
// nodes are kept (see comfyeditor.TrimCatalog). Run it from the repository
// root, review the printed report, and commit both catalog files together.
//
// gen 命令从一份原始的 ComfyUI /object_info 导出重新生成编辑器编译进来的核心节点
// 目录。导出可以来自装了自定义节点的实例；只有核心节点会被保留（见
// comfyeditor.TrimCatalog）。在仓库根目录运行，检查打印出的报告，再把两个目录文件
// 一起提交。
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"AIServeWeave/service/aiServeWeaveControlPlane/internal/comfyeditor"
)

func main() {
	in := flag.String("in", "", "raw /object_info export")
	out := flag.String("out", "service/aiServeWeaveControlPlane/internal/comfyeditor/catalog", "catalog directory")
	comfyui := flag.String("comfyui", "", "ComfyUI version the export came from (system_stats comfyui_version)")
	frontend := flag.String("frontend", "", "ComfyUI_frontend version that ComfyUI requires (system_stats required_frontend_version)")
	apiNodes := flag.Bool("include-api-nodes", false, "keep comfy_api_nodes.* (paid hosted-API nodes)")
	flag.Parse()
	if err := run(*in, *out, *comfyui, *frontend, *apiNodes); err != nil {
		fmt.Fprintln(os.Stderr, "gen:", err)
		os.Exit(1)
	}
}

func run(in, out, comfyui, frontend string, apiNodes bool) error {
	if in == "" || comfyui == "" || frontend == "" {
		return fmt.Errorf("-in, -comfyui and -frontend are required")
	}
	raw, err := os.ReadFile(in)
	if err != nil {
		return err
	}
	catalog, report, err := comfyeditor.TrimCatalog(raw, comfyeditor.TrimOptions{IncludeAPINodes: apiNodes})
	if err != nil {
		return err
	}
	meta, err := json.MarshalIndent(comfyeditor.CatalogMeta{
		ComfyUIVersion: comfyui, FrontendVersion: frontend, Generated: true,
		NodeCount: report.Kept, Modules: report.Modules,
	}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(out, "core.json"), catalog, 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(out, "meta.json"), append(meta, '\n'), 0o644); err != nil {
		return err
	}
	summary, _ := json.MarshalIndent(report, "", "  ")
	fmt.Printf("%s\ncatalog: %d bytes\n", summary, len(catalog))
	return nil
}
