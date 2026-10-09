package instance

import (
	"context"
	"testing"

	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestResources_EmptyClaimNamespacesDenyIngress(t *testing.T) {
	g := NewWithT(t)
	policy := renderNetworkPolicy(t, nil)
	if policy == nil {
		return
	}

	ingress, found, err := unstructured.NestedSlice(policy.Object, "spec", "ingress")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(found).To(BeTrue())
	g.Expect(ingress).To(BeEmpty())
	policyTypes, found, err := unstructured.NestedStringSlice(policy.Object, "spec", "policyTypes")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(found).To(BeTrue())
	g.Expect(policyTypes).To(Equal([]string{"Ingress"}))
}

func TestResources_ActiveClaimNamespacesAllowOnlyThoseNamespaces(t *testing.T) {
	g := NewWithT(t)
	policy := renderNetworkPolicy(t, []string{"one", "two"})
	if policy == nil {
		return
	}

	ingress, found, err := unstructured.NestedSlice(policy.Object, "spec", "ingress")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(found).To(BeTrue())
	g.Expect(ingress).To(HaveLen(1))
	if !found || len(ingress) != 1 {
		return
	}
	rule, ok := ingress[0].(map[string]any)
	g.Expect(ok).To(BeTrue())
	if !ok {
		return
	}
	from, found, err := unstructured.NestedSlice(rule, "from")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(found).To(BeTrue())
	g.Expect(from).To(HaveLen(1))
	if !found || len(from) != 1 {
		return
	}
	peer, ok := from[0].(map[string]any)
	g.Expect(ok).To(BeTrue())
	if !ok {
		return
	}
	selector, found, err := unstructured.NestedMap(peer, "namespaceSelector")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(found).To(BeTrue())
	if !found {
		return
	}
	expressions, found, err := unstructured.NestedSlice(selector, "matchExpressions")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(found).To(BeTrue())
	g.Expect(expressions).To(HaveLen(1))
	if !found || len(expressions) != 1 {
		return
	}
	expression, ok := expressions[0].(map[string]any)
	g.Expect(ok).To(BeTrue())
	if !ok {
		return
	}
	values, found, err := unstructured.NestedStringSlice(expression, "values")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(found).To(BeTrue())
	g.Expect(values).To(Equal([]string{"one", "two"}))
}

func renderNetworkPolicy(t *testing.T, allowedNamespaces []string) *unstructured.Unstructured {
	t.Helper()
	g := NewWithT(t)
	resources, err := Resources(context.Background(), Data{
		Namespace:    "test",
		ProviderName: "provider",
		Service:      Service{Name: "provider"},
		PVC:          PVC{Name: "provider", Size: "1Gi"},
		InitDB:       InitDB{ConfigMapName: "provider-initdb"},
		Postgres:     Postgres{Image: "postgres:16", AdminSecretName: "provider-admin", DefaultDatabase: "postgres"},
		Network:      NetworkPolicy{AllowedNamespaces: allowedNamespaces},
	})
	g.Expect(err).NotTo(HaveOccurred())
	if err != nil {
		return nil
	}
	var policy *unstructured.Unstructured
	for i := range resources {
		if resources[i].GetKind() == "NetworkPolicy" {
			policy = &resources[i]
			break
		}
	}
	g.Expect(policy).NotTo(BeNil(), "NetworkPolicy was not rendered")
	return policy
}
