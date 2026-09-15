/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"text/template"

	"gopkg.in/yaml.v3"
)

type korrel8rRulesFile struct {
	Rules []korrel8rRule `yaml:"rules"`
}

type korrel8rRule struct {
	Name  string `yaml:"name"`
	Start struct {
		Domain  string   `yaml:"domain"`
		Classes []string `yaml:"classes"`
	} `yaml:"start"`
	Goal struct {
		Domain  string   `yaml:"domain"`
		Classes []string `yaml:"classes"`
	} `yaml:"goal"`
	Result struct {
		Query string `yaml:"query"`
	} `yaml:"result"`
}

// TestKorrel8rRHOAIInferenceRulesRenderRepresentativeObjects catches a missing
// ConfigMap key, invalid rule file, invalid template, or a rule that no longer
// produces the resource queries on which the built-in Pod telemetry rules rely.
func TestKorrel8rRHOAIInferenceRulesRenderRepresentativeObjects(t *testing.T) {
	rules := loadRHOAIKorrel8rRules(t)

	tests := []struct {
		name     string
		ruleName string
		fixture  string
		want     []string
	}{
		{
			name:     "LLMInferenceService reaches its workload pods",
			ruleName: "LLMInferenceServiceToServingPods",
			fixture:  "testdata/korrel8r/llminferenceservice.json",
			want: []string{
				`k8s:Pod:{"namespace":"inference","labels":{"app.kubernetes.io/name":"llama","app.kubernetes.io/part-of":"llminferenceservice"}}`,
			},
		},
		{
			name:     "LLMInferenceService without a name emits no pod query",
			ruleName: "LLMInferenceServiceToServingPods",
			fixture:  "testdata/korrel8r/llminferenceservice-without-name.json",
		},
		{
			name:     "HTTPRoute reaches its service backends",
			ruleName: "HTTPRouteToBackendService",
			fixture:  "testdata/korrel8r/httproute.json",
			want: []string{
				`k8s:Service:{"namespace":"inference","name":"llama-epp-service"}`,
				`k8s:Service:{"namespace":"shared","name":"llama-vllm-service"}`,
			},
		},
		{
			name:     "HTTPRoute without a backend name emits no service query",
			ruleName: "HTTPRouteToBackendService",
			fixture:  "testdata/korrel8r/httproute-without-backend-name.json",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rule := findKorrel8rRule(t, rules, tt.ruleName)
			got := renderKorrel8rQuery(t, rule.Result.Query, loadKorrel8rFixture(t, tt.fixture))
			if got != strings.Join(tt.want, "\n") {
				t.Fatalf("unexpected rendered query:\nwant:\n%s\n\ngot:\n%s", strings.Join(tt.want, "\n"), got)
			}
		})
	}
}

func loadRHOAIKorrel8rRules(t *testing.T) []korrel8rRule {
	t.Helper()

	configTemplate, err := resourcesFS.ReadFile(Korrel8rConfigTemplate)
	if err != nil {
		t.Fatalf("reading Korrel8r ConfigMap template: %v", err)
	}

	rendered, err := template.Must(template.New("korrel8r-config").Parse(string(configTemplate))).Clone()
	if err != nil {
		t.Fatalf("parsing Korrel8r ConfigMap template: %v", err)
	}
	var out bytes.Buffer
	if err := rendered.Execute(&out, map[string]any{
		"Namespace":              "monitoring",
		"Korrel8rServiceName":    Korrel8rServiceName,
		"Korrel8rMetricsStore":   true,
		"Korrel8rTracesStore":    true,
		"Korrel8rLokiStore":      true,
		"Logs":                   true,
		"ThanosQuerierEndpoint":  "https://thanos.example.test",
		"TempoQueryEndpoint":     "https://tempo.example.test",
		"LokiQueryEndpoint":      "https://loki.example.test",
		"Korrel8rRequestTimeout": "30s",
		"Korrel8rSessionTimeout": "5m",
	}); err != nil {
		t.Fatalf("rendering Korrel8r ConfigMap template: %v", err)
	}

	var configMap struct {
		Data map[string]string `yaml:"data"`
	}
	if err := yaml.Unmarshal(out.Bytes(), &configMap); err != nil {
		t.Fatalf("parsing rendered Korrel8r ConfigMap: %v", err)
	}
	rawRules, ok := configMap.Data["rhai-inference-rules.yaml"]
	if !ok {
		t.Fatal("rendered Korrel8r ConfigMap does not ship RHOAI inference rules")
	}
	if !strings.Contains(configMap.Data["korrel8r.yaml"], "/etc/korrel8r/custom/rhai-inference-rules.yaml") {
		t.Fatal("rendered Korrel8r configuration does not include the shipped RHOAI inference rules")
	}

	var rulesFile korrel8rRulesFile
	if err := yaml.Unmarshal([]byte(rawRules), &rulesFile); err != nil {
		t.Fatalf("parsing shipped RHOAI Korrel8r rules: %v", err)
	}
	if len(rulesFile.Rules) == 0 {
		t.Fatal("shipped RHOAI Korrel8r rules are empty")
	}
	return rulesFile.Rules
}

func findKorrel8rRule(t *testing.T, rules []korrel8rRule, name string) korrel8rRule {
	t.Helper()
	for _, rule := range rules {
		if rule.Name == name {
			return rule
		}
	}
	t.Fatalf("Korrel8r rule %q was not shipped", name)
	return korrel8rRule{}
}

func loadKorrel8rFixture(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading fixture %q: %v", path, err)
	}
	var fixture map[string]any
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatalf("parsing fixture %q: %v", path, err)
	}
	return fixture
}

func renderKorrel8rQuery(t *testing.T, rawTemplate string, data map[string]any) string {
	t.Helper()
	ruleTemplate, err := template.New("rule").Option("missingkey=error").Parse(rawTemplate)
	if err != nil {
		t.Fatalf("parsing Korrel8r rule template: %v", err)
	}
	var out bytes.Buffer
	if err := ruleTemplate.Execute(&out, data); err != nil {
		t.Fatalf("rendering Korrel8r rule template: %v", err)
	}
	return strings.TrimSpace(out.String())
}

func TestKorrel8rRHOAIInferenceRulesUseOnlyApprovedTransitions(t *testing.T) {
	rules := loadRHOAIKorrel8rRules(t)
	wantRules := map[string]struct{}{
		"LLMInferenceServiceToServingPods": {},
		"HTTPRouteToBackendService":        {},
	}
	for _, rule := range rules {
		if _, ok := wantRules[rule.Name]; !ok {
			t.Fatalf("unexpected RHOAI-specific Korrel8r rule %q; use the built-in rules where they already cover the relationship", rule.Name)
		}
		delete(wantRules, rule.Name)
		if rule.Start.Domain != "k8s" && rule.Start.Domain != "metric" && rule.Start.Domain != "log" {
			t.Fatalf("rule %q uses unexpected start domain %q", rule.Name, rule.Start.Domain)
		}
		if rule.Goal.Domain != "k8s" && rule.Goal.Domain != "metric" && rule.Goal.Domain != "trace" {
			t.Fatalf("rule %q uses unexpected goal domain %q", rule.Name, rule.Goal.Domain)
		}
		if strings.Contains(rule.Result.Query, "cluster-prometheus") || strings.Contains(rule.Result.Query, "cluster-loki") || strings.Contains(rule.Result.Query, "platform-tempo") {
			t.Fatalf("rule %q points to a platform observability backend", rule.Name)
		}
		if rule.Start.Domain == "log" && rule.Goal.Domain == "trace" {
			t.Fatalf("rule %q enables LogToTrace without the required structured trace_id contract", rule.Name)
		}
		if rule.Name == "LLMInferenceServiceToServingPods" &&
			(len(rule.Start.Classes) != 1 || rule.Start.Classes[0] != "LLMInferenceService.v1alpha2.serving.kserve.io") {
			t.Fatalf("LLMInferenceService rule must use the live v1alpha2 resource class, got %v", rule.Start.Classes)
		}
	}
	for name := range wantRules {
		t.Fatalf("expected RHOAI-specific Korrel8r rule %q was not shipped", name)
	}
}
