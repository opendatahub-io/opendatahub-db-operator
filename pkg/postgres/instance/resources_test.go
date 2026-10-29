package instance

import (
	"context"
	"testing"

	. "github.com/onsi/gomega"
	infraApi "github.com/opendatahub-io/opendatahub-db-operator/api/infrastructure/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

func TestResources_UseRedHatPostgresContainerContract(t *testing.T) {
	g := NewWithT(t)
	resources, err := Resources(context.Background(), Data{
		Namespace:          "database-system",
		ProviderName:       "internal",
		ServiceAccountName: "internal-postgres",
		Service:            Service{Name: "internal"},
		PVC:                PVC{Name: "internal", Size: "1Gi"},
		InitDB: InitDB{
			ConfigMapName: "internal-initdb",
			Extensions:    []string{"pg_trgm", "vector"},
		},
		Postgres: Postgres{
			Image:           "registry.redhat.io/rhel9/postgresql-16",
			Replicas:        InternalPostgresReplicaCount,
			AdminSecretName: "internal-admin",
			DefaultDatabase: "application",
		},
		TLS: TLS{
			Enabled:           true,
			UsesManagedIssuer: true,
			SecretName:        "internal-tls",
			IssuerName:        "internal-issuer",
			IssuerRef:         &infraApi.CertManagerIssuerRef{Name: "internal-issuer"},
			Certificate:       Certificate{Name: "internal-certificate"},
		},
	})
	g.Expect(err).NotTo(HaveOccurred())

	statefulSet := &appsv1.StatefulSet{}
	configMap := &corev1.ConfigMap{}
	serviceAccount := &corev1.ServiceAccount{}
	for i := range resources {
		switch resources[i].GetKind() {
		case "StatefulSet":
			g.Expect(runtime.DefaultUnstructuredConverter.FromUnstructured(resources[i].Object, statefulSet)).To(Succeed())
		case "ConfigMap":
			g.Expect(runtime.DefaultUnstructuredConverter.FromUnstructured(resources[i].Object, configMap)).To(Succeed())
		case "ServiceAccount":
			g.Expect(runtime.DefaultUnstructuredConverter.FromUnstructured(resources[i].Object, serviceAccount)).To(Succeed())
		}
	}

	g.Expect(serviceAccount.Name).To(Equal("internal-postgres"))
	g.Expect(serviceAccount.AutomountServiceAccountToken).NotTo(BeNil())
	g.Expect(*serviceAccount.AutomountServiceAccountToken).To(BeFalse())
	g.Expect(statefulSet.Spec.Template.Spec.ServiceAccountName).To(Equal("internal-postgres"))
	g.Expect(statefulSet.Spec.Template.Spec.SecurityContext).NotTo(BeNil())
	g.Expect(statefulSet.Spec.Template.Spec.SecurityContext.RunAsUser).NotTo(BeNil())
	g.Expect(*statefulSet.Spec.Template.Spec.SecurityContext.RunAsUser).To(Equal(InternalPostgresUID))
	g.Expect(statefulSet.Spec.Template.Spec.SecurityContext.RunAsGroup).NotTo(BeNil())
	g.Expect(*statefulSet.Spec.Template.Spec.SecurityContext.RunAsGroup).To(Equal(InternalPostgresUID))
	g.Expect(statefulSet.Spec.Template.Spec.SecurityContext.FSGroup).NotTo(BeNil())
	g.Expect(*statefulSet.Spec.Template.Spec.SecurityContext.FSGroup).To(Equal(InternalPostgresUID))
	g.Expect(statefulSet.Spec.Template.Spec.SecurityContext.SeccompProfile).NotTo(BeNil())
	seccomp := statefulSet.Spec.Template.Spec.SecurityContext.SeccompProfile.Type
	g.Expect(seccomp).To(Equal(corev1.SeccompProfileTypeRuntimeDefault))
	g.Expect(statefulSet.Spec.Template.Spec.Containers).To(HaveLen(1))
	container := statefulSet.Spec.Template.Spec.Containers[0]
	g.Expect(container.SecurityContext).NotTo(BeNil())
	g.Expect(container.SecurityContext.AllowPrivilegeEscalation).NotTo(BeNil())
	g.Expect(*container.SecurityContext.AllowPrivilegeEscalation).To(BeFalse())
	g.Expect(container.SecurityContext.Capabilities).NotTo(BeNil())
	g.Expect(container.SecurityContext.Capabilities.Drop).To(Equal([]corev1.Capability{"ALL"}))

	environment := make(map[string]corev1.EnvVar, len(container.Env))
	for _, env := range container.Env {
		environment[env.Name] = env
	}
	g.Expect(environment).To(HaveKey("POSTGRESQL_ADMIN_PASSWORD"))
	g.Expect(environment["POSTGRESQL_ADMIN_PASSWORD"].ValueFrom.SecretKeyRef).NotTo(BeNil())
	g.Expect(environment["POSTGRESQL_ADMIN_PASSWORD"].ValueFrom.SecretKeyRef.Key).To(Equal("password"))
	g.Expect(environment).To(HaveKey("INTERNAL_POSTGRES_DATABASE"))
	g.Expect(environment["INTERNAL_POSTGRES_DATABASE"].ValueFrom.SecretKeyRef).NotTo(BeNil())
	g.Expect(environment["INTERNAL_POSTGRES_DATABASE"].ValueFrom.SecretKeyRef.Key).To(Equal("dbname"))
	g.Expect(environment).NotTo(HaveKey("POSTGRES_USER"))
	g.Expect(environment).NotTo(HaveKey("POSTGRES_PASSWORD"))
	g.Expect(environment).NotTo(HaveKey("POSTGRES_DB"))
	g.Expect(environment).NotTo(HaveKey("PGDATA"))

	mounts := make(map[string]string, len(container.VolumeMounts))
	for _, mount := range container.VolumeMounts {
		mounts[mount.Name] = mount.MountPath
	}
	g.Expect(mounts["data"]).To(Equal("/var/lib/pgsql/data"))
	g.Expect(mounts["initdb"]).To(Equal("/opt/app-root/src/postgresql-init"))
	g.Expect(mounts["tls"]).To(Equal("/var/lib/pgsql/tls"))
	g.Expect(container.Args).NotTo(BeEmpty())
	g.Expect(container.Args[0]).To(Equal("run-postgresql"))
	g.Expect(container.Args).To(ContainElements(
		"ssl_cert_file=/var/lib/pgsql/tls/tls.crt",
		"ssl_key_file=/var/lib/pgsql/tls/tls.key",
	))
	g.Expect(configMap.Data).To(HaveKey("00-init-extensions.sh"))
	initScript := configMap.Data["00-init-extensions.sh"]
	g.Expect(initScript).To(ContainSubstring("createdb --owner=postgres -- \"$INTERNAL_POSTGRES_DATABASE\""))
	g.Expect(initScript).To(ContainSubstring("--dbname=\"$INTERNAL_POSTGRES_DATABASE\""))
	g.Expect(initScript).To(ContainSubstring("CREATE EXTENSION IF NOT EXISTS \"pg_trgm\""))
	g.Expect(initScript).To(ContainSubstring("CREATE EXTENSION IF NOT EXISTS \"vector\""))
}

func TestResources_EmptyClaimNamespacesAllowOnlyOperator(t *testing.T) {
	g := NewWithT(t)
	policy := renderNetworkPolicy(t, "operator-system", nil)
	if policy == nil {
		return
	}

	ingress, found, err := unstructured.NestedSlice(policy.Object, "spec", "ingress")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(found).To(BeTrue())
	g.Expect(ingress).To(HaveLen(1))
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
	operatorPeer, ok := from[0].(map[string]any)
	g.Expect(ok).To(BeTrue())
	if !ok {
		return
	}
	operatorNamespace, found, err := unstructured.NestedString(
		operatorPeer, "namespaceSelector", "matchLabels", "kubernetes.io/metadata.name",
	)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(found).To(BeTrue())
	g.Expect(operatorNamespace).To(Equal("operator-system"))
	operatorLabel, found, err := unstructured.NestedString(
		operatorPeer, "podSelector", "matchLabels", "app.kubernetes.io/name",
	)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(found).To(BeTrue())
	g.Expect(operatorLabel).To(Equal("opendatahub-db-operator"))
	policyTypes, found, err := unstructured.NestedStringSlice(policy.Object, "spec", "policyTypes")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(found).To(BeTrue())
	g.Expect(policyTypes).To(Equal([]string{"Ingress"}))
}

func TestResources_ActiveClaimNamespacesAllowOperatorAndClaimNamespaces(t *testing.T) {
	g := NewWithT(t)
	policy := renderNetworkPolicy(t, "operator-system", []string{"one", "two"})
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
	g.Expect(from).To(HaveLen(2))
	if !found || len(from) != 2 {
		return
	}
	operatorPeer, ok := from[0].(map[string]any)
	g.Expect(ok).To(BeTrue())
	if !ok {
		return
	}
	operatorNamespace, found, err := unstructured.NestedString(
		operatorPeer, "namespaceSelector", "matchLabels", "kubernetes.io/metadata.name",
	)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(found).To(BeTrue())
	g.Expect(operatorNamespace).To(Equal("operator-system"))

	peer, ok := from[1].(map[string]any)
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

func TestResources_NoAllowedSourcesDenyIngress(t *testing.T) {
	g := NewWithT(t)
	policy := renderNetworkPolicy(t, "", nil)
	if policy == nil {
		return
	}

	ingress, found, err := unstructured.NestedSlice(policy.Object, "spec", "ingress")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(found).To(BeTrue())
	g.Expect(ingress).To(BeEmpty())
}

func renderNetworkPolicy(
	t *testing.T,
	operatorNamespace string,
	allowedNamespaces []string,
) *unstructured.Unstructured {
	t.Helper()
	g := NewWithT(t)
	resources, err := Resources(context.Background(), Data{
		Namespace:          "test",
		ProviderName:       "provider",
		ServiceAccountName: "provider-postgres",
		Service:            Service{Name: "provider"},
		PVC:                PVC{Name: "provider", Size: "1Gi"},
		InitDB:             InitDB{ConfigMapName: "provider-initdb"},
		Postgres: Postgres{
			Image:           "registry.redhat.io/rhel9/postgresql-16",
			Replicas:        InternalPostgresReplicaCount,
			AdminSecretName: "provider-admin",
			DefaultDatabase: "postgres",
		},
		Network: NetworkPolicy{OperatorNamespace: operatorNamespace, AllowedNamespaces: allowedNamespaces},
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
