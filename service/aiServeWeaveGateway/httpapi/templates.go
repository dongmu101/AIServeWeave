package httpapi

import (
	"sort"

	"AIServeWeave/common/workflowview"
	"AIServeWeave/service/aiServeWeaveGateway/workflow"
)

// renderTemplates converts the registry into the catalogue form, naming every
// field that may cross.
//
// The graph is the field that must not, and it is left out here rather than
// stripped later: a rendering that started from the whole Template and removed
// the graph would put the repository's security rule one forgotten line away
// from being broken. The node and field an input writes are left out for a
// smaller reason — they are graph structure too, and a caller substitutes by
// name.
//
// renderTemplates 把注册表转换成目录形式，逐一点名可以外传的每个字段。
//
// 图是那个绝不能外传的字段，它在这里是压根没被取用，而不是事后被剥掉：一个从完整
// Template 出发再删掉图的渲染，会让仓库的安全规则距离被打破只差一行被遗忘的代码。输入
// 所写入的节点与字段被略去则是出于一个更小的理由——它们同样是图结构，而调用方是按名字
// 替换的。
func renderTemplates(handle *workflow.Handle) []workflowview.Template {
	if handle == nil {
		return []workflowview.Template{}
	}

	ids := handle.IDs()
	out := make([]workflowview.Template, 0, len(ids))
	for _, id := range ids {
		template, ok := handle.Lookup(id)
		if !ok {
			continue
		}
		view := workflowview.Template{
			ID:               template.ID,
			Description:      template.Description,
			Inputs:           make([]workflowview.Input, 0, len(template.Inputs)),
			Outputs:          make([]workflowview.Output, 0, len(template.Outputs)),
			Dependencies:     template.Dependencies,
			Version:          template.Version,
			VisibleTenantIDs: template.VisibleTenantIDs,
			Valid:            true,
		}
		// A registry only holds templates that validated at load, so this
		// re-check is expected to pass. It runs anyway because the catalogue
		// claims the templates are valid, and a claim nobody checks is one
		// that quietly stops being true.
		//
		// 注册表只持有加载时通过校验的模板，因此这次复查预期会通过。仍然执行它，是
		// 因为目录声称这些模板有效，而一个没人检查的声称，会悄无声息地不再成立。
		if err := template.Validate(); err != nil {
			view.Valid = false
			view.ValidationError = err.Error()
		}
		for _, input := range template.Inputs {
			view.Inputs = append(view.Inputs, workflowview.Input{
				Name:      input.Name,
				Type:      string(input.Type),
				Required:  input.Required,
				Default:   input.Default,
				MaxLength: input.MaxLength,
				Min:       input.Min,
				Max:       input.Max,
			})
		}
		for _, output := range template.Outputs {
			view.Outputs = append(view.Outputs, workflowview.Output{
				Name:        output.Name,
				Type:        output.Type,
				Description: output.Description,
			})
		}
		out = append(out, view)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
