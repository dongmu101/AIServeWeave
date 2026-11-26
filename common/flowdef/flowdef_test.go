package flowdef

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// valid returns a small runnable flow: draft an answer, and if it still says
// TODO, pass it through a template before the output.
//
//	start → draft → check ─true→ fix → end
//	                      └false────────↗
func valid() Definition {
	return Definition{
		Inputs: []Input{{Name: "topic", Label: "主题", Type: TypeText, Required: true}},
		Nodes: []Node{
			{ID: "start", Type: NodeInput},
			{ID: "draft", Type: NodeLLM, Config: json.RawMessage(`{"model":"qwen3","prompt":"Write about {{inputs.topic}}"}`)},
			{ID: "check", Type: NodeCondition, Config: json.RawMessage(`{"left":"{{draft.text}}","op":"contains","right":"TODO"}`)},
			{ID: "fix", Type: NodeTemplate, Config: json.RawMessage(`{"template":"fixed: {{draft.text}}"}`)},
			{ID: "end", Type: NodeOutput},
		},
		Edges: []Edge{
			{From: "start", To: "draft"},
			{From: "draft", To: "check"},
			{From: "check", To: "fix", Branch: BranchTrue},
			{From: "check", To: "end", Branch: BranchFalse},
			{From: "fix", To: "end"},
		},
		Outputs: []Output{{Name: "answer", Ref: "{{draft.text}}"}},
	}
}

func setConfig(def *Definition, id, config string) {
	for i := range def.Nodes {
		if def.Nodes[i].ID == id {
			def.Nodes[i].Config = json.RawMessage(config)
			return
		}
	}
	panic("no node " + id)
}

type want struct {
	node string
	code IssueCode
}

func TestValidate(t *testing.T) {
	for _, tt := range []struct {
		name   string
		mutate func(*Definition)
		want   []want // empty: the definition must be valid
	}{
		{"valid", func(*Definition) {}, nil},
		{"structured output fields are referenceable", func(d *Definition) {
			setConfig(d, "draft", `{"model":"qwen3","system":"Be brief.","prompt":"{{inputs.topic}}","temperature":0.2,"max_tokens":512,
				"json_fields":[{"name":"score","type":"number","description":"0-10"},{"name":"ok","type":"boolean"}]}`)
			setConfig(d, "check", `{"left":"{{draft.score}}","op":"gte","right":"7"}`)
			d.Outputs = append(d.Outputs, Output{Name: "ok", Ref: "{{draft.ok}}"})
		}, nil},
		{"both branches to one node", func(d *Definition) {
			d.Edges[3] = Edge{From: "check", To: "fix", Branch: BranchFalse}
		}, nil},
		{"input default of the right type", func(d *Definition) {
			d.Inputs = append(d.Inputs, Input{Name: "n", Type: TypeNumber, Default: json.RawMessage(`3`)},
				Input{Name: "b", Type: TypeBoolean, Default: json.RawMessage(`null`)})
		}, nil},
		{"input and output nodes accept an empty config", func(d *Definition) {
			setConfig(d, "start", `{}`)
			setConfig(d, "end", `null`)
		}, nil},

		// Anchors.
		{"no input node", func(d *Definition) {
			d.Nodes = d.Nodes[1:]
			d.Edges = d.Edges[1:]
		}, []want{{"", CodeMissingNode}}},
		{"two output nodes", func(d *Definition) {
			d.Nodes = append(d.Nodes, Node{ID: "end2", Type: NodeOutput})
			d.Edges = append(d.Edges, Edge{From: "fix", To: "end2"})
		}, []want{{"end2", CodeMultipleNodes}}},
		{"input node with config", func(d *Definition) { setConfig(d, "start", `{"x":1}`) }, []want{{"start", CodeInvalidConfig}}},

		// Node identity.
		{"duplicate node id", func(d *Definition) {
			d.Nodes = append(d.Nodes, Node{ID: "fix", Type: NodeTemplate, Config: json.RawMessage(`{"template":"x"}`)})
		}, []want{{"fix", CodeDuplicateName}}},
		{"invalid node id", func(d *Definition) {
			d.Nodes = append(d.Nodes, Node{ID: "bad-id", Type: NodeTemplate, Config: json.RawMessage(`{"template":"x"}`)})
		}, []want{{"", CodeInvalidName}}},
		{"reserved node id", func(d *Definition) {
			d.Nodes = append(d.Nodes, Node{ID: InputsScope, Type: NodeTemplate, Config: json.RawMessage(`{"template":"x"}`)})
		}, []want{{InputsScope, CodeInvalidName}}},
		{"unknown node type", func(d *Definition) { d.Nodes[3].Type = "image_generation" }, []want{{"fix", CodeInvalidType}}},
		{"label too long", func(d *Definition) { d.Nodes[1].Label = strings.Repeat("长", MaxLabelRunes+1) }, []want{{"draft", CodeInvalidLabel}}},

		// LLM config.
		{"llm without config", func(d *Definition) { setConfig(d, "draft", ``) }, []want{{"draft", CodeInvalidConfig}}},
		{"llm unknown config field", func(d *Definition) {
			setConfig(d, "draft", `{"model":"qwen3","prompt":"x","temprature":1}`)
		}, []want{{"draft", CodeInvalidConfig}}},
		{"llm without model", func(d *Definition) { setConfig(d, "draft", `{"model":" ","prompt":"x"}`) }, []want{{"draft", CodeRequired}}},
		{"llm model is a template", func(d *Definition) {
			setConfig(d, "draft", `{"model":"{{inputs.topic}}","prompt":"x"}`)
		}, []want{{"draft", CodeInvalidConfig}}},
		{"llm model with spaces around it", func(d *Definition) { setConfig(d, "draft", `{"model":" qwen3","prompt":"x"}`) }, []want{{"draft", CodeInvalidConfig}}},
		{"llm without prompt", func(d *Definition) { setConfig(d, "draft", `{"model":"qwen3","prompt":""}`) }, []want{{"draft", CodeRequired}}},
		{"llm temperature out of range", func(d *Definition) {
			setConfig(d, "draft", `{"model":"qwen3","prompt":"x","temperature":2.5}`)
		}, []want{{"draft", CodeOutOfRange}}},
		{"llm max_tokens zero", func(d *Definition) {
			setConfig(d, "draft", `{"model":"qwen3","prompt":"x","max_tokens":0}`)
		}, []want{{"draft", CodeOutOfRange}}},
		{"json field named text", func(d *Definition) {
			setConfig(d, "draft", `{"model":"qwen3","prompt":"x","json_fields":[{"name":"text","type":"text"}]}`)
		}, []want{{"draft", CodeInvalidName}}},
		{"duplicate json field", func(d *Definition) {
			setConfig(d, "draft", `{"model":"qwen3","prompt":"x","json_fields":[{"name":"a","type":"text"},{"name":"a","type":"number"}]}`)
		}, []want{{"draft", CodeDuplicateName}}},
		{"json field of unknown type", func(d *Definition) {
			setConfig(d, "draft", `{"model":"qwen3","prompt":"x","json_fields":[{"name":"a","type":"object"}]}`)
		}, []want{{"draft", CodeInvalidType}}},
		{"nested reference", func(d *Definition) {
			setConfig(d, "draft", `{"model":"qwen3","prompt":"{{inputs.topic.x}}"}`)
		}, []want{{"draft", CodeInvalidTemplate}}},
		{"template too long", func(d *Definition) {
			setConfig(d, "fix", fmt.Sprintf(`{"template":%q}`, strings.Repeat("x", MaxTemplateBytes+1)))
		}, []want{{"fix", CodeInvalidTemplate}}},

		// Template and condition config.
		{"empty template", func(d *Definition) { setConfig(d, "fix", `{"template":"  "}`) }, []want{{"fix", CodeRequired}}},
		{"template unknown config field", func(d *Definition) { setConfig(d, "fix", `{"text":"x"}`) }, []want{{"fix", CodeInvalidConfig}}},
		{"too many json fields", func(d *Definition) {
			fields := make([]string, MaxJSONFields+1)
			for i := range fields {
				fields[i] = fmt.Sprintf(`{"name":"f%d","type":"text"}`, i)
			}
			setConfig(d, "draft", `{"model":"qwen3","prompt":"x","json_fields":[`+strings.Join(fields, ",")+`]}`)
		}, []want{{"draft", CodeTooMany}}},
		{"unknown operator", func(d *Definition) { setConfig(d, "check", `{"left":"{{draft.text}}","op":"matches","right":"x"}`) }, []want{{"check", CodeInvalidConfig}}},
		{"empty operator with a right operand", func(d *Definition) {
			setConfig(d, "check", `{"left":"{{draft.text}}","op":"empty","right":"x"}`)
		}, []want{{"check", CodeInvalidConfig}}},
		{"empty operator alone", func(d *Definition) { setConfig(d, "check", `{"left":"{{draft.text}}","op":"not_empty"}`) }, nil},
		{"binary operator without right operand", func(d *Definition) {
			setConfig(d, "check", `{"left":"{{draft.text}}","op":"equals"}`)
		}, []want{{"check", CodeRequired}}},
		{"numeric operator with a non-number constant", func(d *Definition) {
			setConfig(d, "check", `{"left":"{{draft.text}}","op":"gt","right":"abc"}`)
		}, []want{{"check", CodeInvalidConfig}}},
		{"numeric operator with references on both sides", func(d *Definition) {
			setConfig(d, "check", `{"left":"{{draft.text}}","op":"lt","right":"{{inputs.topic}}"}`)
		}, nil},

		// Edges.
		{"edge to a missing node", func(d *Definition) { d.Edges = append(d.Edges, Edge{From: "fix", To: "nowhere"}) }, []want{{"fix", CodeInvalidEdge}}},
		{"edge to itself", func(d *Definition) { d.Edges = append(d.Edges, Edge{From: "fix", To: "fix"}) }, []want{{"fix", CodeInvalidEdge}}},
		{"edge into the input node", func(d *Definition) { d.Edges = append(d.Edges, Edge{From: "fix", To: "start"}) }, []want{{"fix", CodeInvalidEdge}}},
		{"edge out of the output node", func(d *Definition) {
			d.Nodes = append(d.Nodes, Node{ID: "after", Type: NodeTemplate, Config: json.RawMessage(`{"template":"x"}`)})
			d.Edges = append(d.Edges, Edge{From: "end", To: "after"})
		}, []want{{"end", CodeInvalidEdge}, {"after", CodeUnreachable}}},
		{"condition edge without branch", func(d *Definition) { d.Edges[2].Branch = "" }, []want{{"check", CodeInvalidEdge}}},
		{"branch on a non-condition edge", func(d *Definition) { d.Edges[1].Branch = BranchTrue }, []want{{"draft", CodeInvalidEdge}}},
		{"duplicate edge", func(d *Definition) { d.Edges = append(d.Edges, Edge{From: "start", To: "draft"}) }, []want{{"start", CodeInvalidEdge}}},
		{"cycle", func(d *Definition) {
			d.Edges = append(d.Edges, Edge{From: "fix", To: "draft"})
		}, []want{{"draft", CodeCycle}, {"check", CodeCycle}, {"fix", CodeCycle}}},
		{"unreachable node", func(d *Definition) {
			d.Nodes = append(d.Nodes, Node{ID: "orphan", Type: NodeTemplate, Config: json.RawMessage(`{"template":"x"}`)})
			d.Edges = append(d.Edges, Edge{From: "orphan", To: "end"})
		}, []want{{"orphan", CodeUnreachable}}},

		// References.
		{"undeclared input", func(d *Definition) {
			setConfig(d, "draft", `{"model":"qwen3","prompt":"{{inputs.subject}}"}`)
		}, []want{{"draft", CodeUnknownReference}}},
		{"missing node", func(d *Definition) { setConfig(d, "fix", `{"template":"{{ghost.text}}"}`) }, []want{{"fix", CodeUnknownReference}}},
		{"field the node does not produce", func(d *Definition) { setConfig(d, "fix", `{"template":"{{draft.score}}"}`) }, []want{{"fix", CodeUnknownReference}}},
		{"input node fields are not referenceable", func(d *Definition) {
			setConfig(d, "fix", `{"template":"{{start.topic}}"}`)
		}, []want{{"fix", CodeUnknownReference}}},
		{"downstream node", func(d *Definition) {
			setConfig(d, "draft", `{"model":"qwen3","prompt":"{{fix.text}}"}`)
		}, []want{{"draft", CodeNotUpstream}}},
		{"own output", func(d *Definition) { setConfig(d, "fix", `{"template":"{{fix.text}}"}`) }, []want{{"fix", CodeNotUpstream}}},
		{"output from a side branch", func(d *Definition) {
			d.Nodes = append(d.Nodes, Node{ID: "side", Type: NodeTemplate, Config: json.RawMessage(`{"template":"x"}`)})
			d.Edges = append(d.Edges, Edge{From: "start", To: "side"})
			d.Outputs[0].Ref = "{{side.text}}"
		}, []want{{"end", CodeNotUpstream}}},
		{"references are still checked in a cycle", func(d *Definition) {
			d.Edges = append(d.Edges, Edge{From: "fix", To: "draft"})
			setConfig(d, "fix", `{"template":"{{ghost.text}}"}`)
		}, []want{{"fix", CodeCycle}, {"fix", CodeUnknownReference}}},

		// Inputs and outputs.
		{"invalid input name", func(d *Definition) { d.Inputs[0].Name = "my topic" }, []want{{"start", CodeInvalidName}, {"draft", CodeUnknownReference}}},
		{"duplicate input", func(d *Definition) { d.Inputs = append(d.Inputs, Input{Name: "topic", Type: TypeText}) }, []want{{"start", CodeDuplicateName}}},
		{"input of unknown type", func(d *Definition) { d.Inputs[0].Type = "image" }, []want{{"start", CodeInvalidType}}},
		{"input default of the wrong type", func(d *Definition) { d.Inputs[0].Default = json.RawMessage(`42`) }, []want{{"start", CodeInvalidDefault}}},
		{"number and boolean defaults of the wrong type", func(d *Definition) {
			d.Inputs = append(d.Inputs, Input{Name: "n", Type: TypeNumber, Default: json.RawMessage(`"3"`)},
				Input{Name: "b", Type: TypeBoolean, Default: json.RawMessage(`1`)})
		}, []want{{"start", CodeInvalidDefault}}},
		{"duplicate output", func(d *Definition) { d.Outputs = append(d.Outputs, Output{Name: "answer", Ref: "x"}) }, []want{{"end", CodeDuplicateName}}},
		{"output without ref", func(d *Definition) { d.Outputs[0].Ref = "" }, []want{{"end", CodeRequired}}},

		// Bounds.
		{"too many nodes", func(d *Definition) {
			for i := len(d.Nodes); i <= MaxNodes; i++ {
				d.Nodes = append(d.Nodes, Node{ID: fmt.Sprintf("n%d", i), Type: "bogus"})
			}
		}, []want{{"", CodeTooMany}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			def := valid()
			tt.mutate(&def)
			err := Validate(def)
			if len(tt.want) == 0 {
				if err != nil {
					t.Fatalf("Validate() = %v, want valid", err)
				}
				return
			}
			var verr *ValidationError
			if !errors.As(err, &verr) {
				t.Fatalf("Validate() = %v, want a *ValidationError", err)
			}
			for _, w := range tt.want {
				if !hasIssue(verr.Issues, w) {
					t.Errorf("missing issue %+v in %+v", w, verr.Issues)
				}
			}
		})
	}
}

func hasIssue(issues []Issue, w want) bool {
	for _, is := range issues {
		if is.Node == w.node && is.Code == w.code {
			return true
		}
	}
	return false
}

// TestValidateReportsOnlyTheBoundOnHugeDefinitions keeps an over-limit
// definition from costing a full validation pass.
func TestValidateReportsOnlyTheBoundOnHugeDefinitions(t *testing.T) {
	def := valid()
	for i := 0; i <= MaxEdges; i++ {
		def.Edges = append(def.Edges, Edge{From: "ghost", To: "ghost"})
	}
	var verr *ValidationError
	if !errors.As(Validate(def), &verr) || len(verr.Issues) != 1 || verr.Issues[0].Code != CodeTooMany {
		t.Fatalf("Validate() issues = %+v, want only %s", verr, CodeTooMany)
	}
}

func TestValidateCapsIssues(t *testing.T) {
	var def Definition
	for i := 0; i < MaxNodes; i++ {
		def.Nodes = append(def.Nodes, Node{ID: fmt.Sprintf("n%d", i), Type: "bogus"})
	}
	var verr *ValidationError
	if !errors.As(Validate(def), &verr) {
		t.Fatal("Validate() accepted a definition of unknown nodes")
	}
	if len(verr.Issues) != MaxIssues || !verr.Truncated {
		t.Fatalf("got %d issues, truncated=%v; want %d, truncated", len(verr.Issues), verr.Truncated, MaxIssues)
	}
}

// TestValidateNeverQuotesContent guards the rule that prompts and values stay
// out of error text: only identifiers may appear.
func TestValidateNeverQuotesContent(t *testing.T) {
	const secret = "SECRET-PROMPT-TEXT"
	def := valid()
	def.Nodes[1].Label = secret + strings.Repeat("x", MaxLabelRunes)
	def.Inputs[0].Default = json.RawMessage(`"` + secret + strings.Repeat("x", MaxTextValueBytes) + `"`)
	setConfig(&def, "draft", `{"model":"qwen3","prompt":"`+secret+` {{inputs.nope}} {{a.b.c}}"}`)
	setConfig(&def, "check", `{"left":"`+secret+`","op":"gt","right":"`+secret+`"}`)
	def.Edges = append(def.Edges, Edge{From: secret, To: secret + "!"})

	err := Validate(def)
	if err == nil {
		t.Fatal("Validate() accepted an invalid definition")
	}
	var verr *ValidationError
	errors.As(err, &verr)
	body, _ := json.Marshal(verr.Issues)
	for _, text := range []string{err.Error(), string(body)} {
		if strings.Contains(text, secret) {
			t.Fatalf("error text quotes flow content: %s", text)
		}
	}
	if len(verr.Issues) < 6 {
		t.Fatalf("expected an issue per planted problem, got %+v", verr.Issues)
	}
}

func TestDecode(t *testing.T) {
	good, err := json.Marshal(valid())
	if err != nil {
		t.Fatal(err)
	}
	def, err := Decode(good)
	if err != nil {
		t.Fatalf("Decode(valid) = %v", err)
	}
	if err := Validate(def); err != nil {
		t.Fatalf("Validate(Decode(valid)) = %v", err)
	}

	for name, raw := range map[string]string{
		"unknown field":   `{"nodes":[],"extra":1}`,
		"trailing data":   `{"nodes":[]} {}`,
		"not an object":   `[]`,
		"wrong node type": `{"nodes":[{"id":1}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			var verr *ValidationError
			if _, err := Decode([]byte(raw)); !errors.As(err, &verr) || verr.Issues[0].Code != CodeInvalidJSON {
				t.Fatalf("Decode(%s) = %v, want %s", raw, err, CodeInvalidJSON)
			}
		})
	}

	var verr *ValidationError
	huge := make([]byte, MaxDefinitionBytes+1)
	if _, err := Decode(huge); !errors.As(err, &verr) || verr.Issues[0].Code != CodeTooLarge {
		t.Fatalf("Decode(huge) = %v, want %s", err, CodeTooLarge)
	}
}

func TestNodeConfigAccessorsCheckTheType(t *testing.T) {
	n := valid().Nodes[1] // llm
	if _, err := n.LLM(); err != nil {
		t.Fatalf("LLM() = %v", err)
	}
	if _, err := n.Template(); err == nil {
		t.Fatal("Template() decoded an llm node")
	}
	if _, err := n.Condition(); err == nil {
		t.Fatal("Condition() decoded an llm node")
	}
}

func TestBindInputs(t *testing.T) {
	inputs := []Input{
		{Name: "topic", Type: TypeText, Required: true},
		{Name: "tone", Type: TypeText, Default: json.RawMessage(`"plain"`)},
		{Name: "count", Type: TypeNumber, Default: json.RawMessage(`3`)},
		{Name: "strict", Type: TypeBoolean},
	}
	got, err := BindInputs(inputs, map[string]json.RawMessage{
		"topic": json.RawMessage(`"cats"`), "count": json.RawMessage(`2.50`), "tone": json.RawMessage(`null`),
	})
	if err != nil {
		t.Fatalf("BindInputs() = %v", err)
	}
	wantValues := map[string]string{"topic": "cats", "tone": "plain", "count": "2.5", "strict": ""}
	if fmt.Sprint(got) != fmt.Sprint(wantValues) {
		t.Fatalf("BindInputs() = %v, want %v", got, wantValues)
	}

	for _, tt := range []struct {
		name     string
		supplied map[string]json.RawMessage
		field    string
		code     IssueCode
	}{
		{"missing required", map[string]json.RawMessage{}, "inputs.topic", CodeRequired},
		{"blank required", map[string]json.RawMessage{"topic": json.RawMessage(`"  "`)}, "inputs.topic", CodeRequired},
		{"wrong type", map[string]json.RawMessage{"topic": json.RawMessage(`"x"`), "strict": json.RawMessage(`"yes"`)}, "inputs.strict", CodeInvalidValue},
		{"undeclared", map[string]json.RawMessage{"topic": json.RawMessage(`"x"`), "topik": json.RawMessage(`"x"`)}, "inputs", CodeUnknownInput},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := BindInputs(inputs, tt.supplied)
			var verr *ValidationError
			if !errors.As(err, &verr) || len(verr.Issues) != 1 || verr.Issues[0].Field != tt.field || verr.Issues[0].Code != tt.code {
				t.Fatalf("BindInputs() = %+v, want one %s on %s", verr, tt.code, tt.field)
			}
		})
	}
}
