package manifests

import (
	"os"
	"strings"
	"testing"
)

func TestOperatorRBACCoversEmbeddedOperandRoles(t *testing.T) {
	operands, err := os.ReadFile("../operator/operands.json")
	if err != nil {
		t.Fatal(err)
	}
	rbac, err := os.Open("../../config/rbac.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer rbac.Close()
	if err := CheckDelegatedRBAC(operands, rbac); err != nil {
		t.Fatal(err)
	}
}

func TestCheckDelegatedRBAC(t *testing.T) {
	const operands = `[{"kind":"ClusterRole","metadata":{"name":"hammerspace-csi-node"},
		"rules":[{"apiGroups":[""],"resources":["nodes"],"verbs":["get","update"]}]}]`
	for _, tc := range []struct {
		name, rbac, wantErr string
	}{
		{"covered across rules", `
kind: ClusterRole
rules:
- apiGroups: [""]
  resources: [nodes]
  verbs: [get]
- apiGroups: [""]
  resources: [nodes, pods]
  verbs: [update, list]
`, ""},
		{"missing verb", `
kind: ClusterRole
rules:
- apiGroups: [""]
  resources: [nodes]
  verbs: [get]
`, `update ""/nodes`},
		{"named grant does not cover", `
kind: ClusterRole
rules:
- apiGroups: [""]
  resources: [nodes]
  verbs: [get, update]
  resourceNames: [worker-0]
`, `get ""/nodes`},
		{"escalate rejected", `
kind: ClusterRole
rules:
- apiGroups: [""]
  resources: [nodes]
  verbs: [get, update, escalate]
`, "escalate"},
		{"no cluster role", `
kind: Role
rules: []
`, "no ClusterRole"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckDelegatedRBAC([]byte(operands), strings.NewReader(tc.rbac))
			if tc.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("got %v, want error containing %q", err, tc.wantErr)
			}
		})
	}
}
