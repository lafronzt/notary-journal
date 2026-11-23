package journal

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestHelmDeploymentConfigurations(t *testing.T) {
	helm, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("helm is not installed")
	}
	chart := filepath.Join("..", "..", "deploy", "helm", "notary-journal")
	run := func(args ...string) (string, error) {
		t.Helper()
		out, err := exec.Command(helm, args...).CombinedOutput()
		return string(out), err
	}
	if out, err := run("lint", "--strict", chart, "--set", "auth.existingSecret=journal-auth"); err != nil {
		t.Fatalf("helm lint: %v\n%s", err, out)
	}
	base := []string{"template", "journal", chart, "--set", "auth.existingSecret=journal-auth"}
	out, err := run(base...)
	if err != nil {
		t.Fatal(out, err)
	}
	for _, required := range []string{"kind: StatefulSet", "replicas: 1", "whenDeleted: Retain", "volumeClaimTemplates:", "runAsNonRoot: true", "readOnlyRootFilesystem: true", "path: /readyz", "path: /healthz", "secretName: journal-auth", "value: /var/run/notary-auth/password"} {
		if !strings.Contains(out, required) {
			t.Errorf("default chart missing %q", required)
		}
	}
	if strings.Contains(out, "NOTARY_NAME") {
		t.Error("notary defaults rendered without values")
	}
	withNotary, err := run(append(append([]string{}, base...), "--set", "notary.name=Alex Rivera", "--set", "notary.commissionId=01XX0000000")...)
	if err != nil || !strings.Contains(withNotary, "name: NOTARY_NAME") || !strings.Contains(withNotary, "01XX0000000") {
		t.Fatalf("notary defaults not rendered: %v\n%s", err, withNotary)
	}
	args := append(append([]string{}, base...), "--set", "ingress.enabled=true", "--set", "ingress.host=journal.example.com", "--set", "publicURL=https://journal.example.com", "--set", "persistence.existingClaim=existing-journal", "--set", "gcs.credentialsSecret=google-credentials", "--set", "gcs.bucket=example-notary", "--set", "networkPolicy.enabled=true")
	out, err = run(args...)
	if err != nil {
		t.Fatal(out, err)
	}
	for _, required := range []string{"kind: Ingress", "kind: NetworkPolicy", "claimName: existing-journal", "value: \"https://journal.example.com\"", "secretName: google-credentials", "value: /var/run/gcs/credentials.json"} {
		if !strings.Contains(out, required) {
			t.Errorf("ingress chart missing %q", required)
		}
	}
	if strings.Contains(out, "volumeClaimTemplates:") {
		t.Fatal("existing PVC configuration created a second claim")
	}
	for name, extra := range map[string][]string{
		"no password secret":         {"--set", "auth.existingSecret="},
		"multiple writers":           {"--set", "replicaCount=2"},
		"insecure ingress":           {"--set", "ingress.enabled=true", "--set", "ingress.host=journal.example.com"},
		"mismatched origin":          {"--set", "ingress.enabled=true", "--set", "ingress.host=other.example.com", "--set", "publicURL=https://journal.example.com"},
		"shared writable filesystem": {"--set", "persistence.accessModes[0]=ReadWriteMany"},
	} {
		t.Run(name, func(t *testing.T) {
			args := append(append([]string{}, base...), extra...)
			if out, err := run(args...); err == nil {
				t.Fatalf("invalid configuration accepted:\n%s", out)
			}
		})
	}
}
