package reconcilers

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
)

// The values a default chart install must set.
const chartRequiredValues = "frontend.keycloak.url=https://keycloak.example.com," +
	"keyManager.nebariApp.hostname=llm-keys.example.com"

// The API server lets the operator create the EPP Role only if the operator
// already holds every permission in it (RBAC escalation prevention, #121).
func TestOperatorClusterRolesHoldEPPRole(t *testing.T) {
	t.Parallel()
	pool, err := BuildInferencePoolResources(defaultInferencePoolModel(), defaultInferencePoolConfig())
	if err != nil {
		t.Fatal(err)
	}
	eppRules := pool.EPPRole.Rules
	repoRoot := filepath.Join("..", "..", "..", "..")

	t.Run("dev manifest", func(t *testing.T) {
		t.Parallel()
		data, err := os.ReadFile(filepath.Join(repoRoot, "dev", "manifests", "operator.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		assertClusterRoleHolds(t, data, "llm-operator", eppRules)
	})

	for _, tc := range []struct{ name, values string }{
		{"chart", chartRequiredValues},
		{"chart with usage.enabled", chartRequiredValues + ",usage.enabled=true,usage.openmeter.url=http://openmeter.example.com"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			helm, err := exec.LookPath("helm")
			if err != nil {
				t.Skip("helm is not on PATH, so the chart's operator ClusterRole is not checked")
			}
			chart := filepath.Join(repoRoot, "charts", "nebari-llm-serving")
			cmd := exec.Command(helm, "template", "nebari-llm-serving", chart, "--set", tc.values)
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			out, err := cmd.Output()
			if err != nil {
				t.Fatalf("helm template: %v\n%s", err, stderr.String())
			}
			assertClusterRoleHolds(t, out, "nebari-llm-serving-operator", eppRules)
		})
	}
}

// assertClusterRoleHolds fails unless the named ClusterRole in manifests
// grants every group, resource and verb in rules.
func assertClusterRoleHolds(t *testing.T, manifests []byte, name string, rules []rbacv1.PolicyRule) {
	t.Helper()
	dec := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader(manifests), 4096)
	for {
		var role rbacv1.ClusterRole
		err := dec.Decode(&role)
		if errors.Is(err, io.EOF) {
			t.Fatalf("no ClusterRole %q in the manifests", name)
		}
		if err != nil {
			t.Fatal(err)
		}
		if role.Kind != "ClusterRole" || role.Name != name {
			continue
		}
		if missing := missingGrants(role.Rules, rules); len(missing) > 0 {
			t.Errorf("ClusterRole %q lacks EPP Role permissions; creating the Role fails with "+
				"\"attempting to grant RBAC permissions not currently held\":\n%s", name, strings.Join(missing, "\n"))
		}
		return
	}
}

// missingGrants lists, sorted, each group, resource and verb in want that no
// rule in held grants.
func missingGrants(held, want []rbacv1.PolicyRule) []string {
	var missing []string
	for _, rule := range want {
		for _, group := range rule.APIGroups {
			for _, resource := range rule.Resources {
				for _, verb := range rule.Verbs {
					if !holds(held, group, resource, verb) {
						missing = append(missing, fmt.Sprintf("{APIGroups:[%q], Resources:[%q], Verbs:[%q]}", group, resource, verb))
					}
				}
			}
		}
	}
	slices.Sort(missing)
	return missing
}

// holds reports whether a rule in held allows verb on all objects of
// group/resource. A rule limited by resourceNames does not.
func holds(held []rbacv1.PolicyRule, group, resource, verb string) bool {
	return slices.ContainsFunc(held, func(rule rbacv1.PolicyRule) bool {
		return len(rule.ResourceNames) == 0 && matchesRBAC(rule.APIGroups, group) &&
			matchesRBAC(rule.Resources, resource) && matchesRBAC(rule.Verbs, verb)
	})
}

func matchesRBAC(values []string, value string) bool {
	return slices.Contains(values, value) || slices.Contains(values, "*")
}
