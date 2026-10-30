package databaseservice

import (
	"context"
	"errors"
	"testing"

	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"

	infraApi "github.com/opendatahub-io/opendatahub-db-operator/api/infrastructure/v1alpha1"
	servicesApi "github.com/opendatahub-io/opendatahub-db-operator/api/services/v1alpha1"
	moduleconfig "github.com/opendatahub-io/opendatahub-db-operator/pkg/config"
	dbmetrics "github.com/opendatahub-io/opendatahub-db-operator/pkg/metrics"
)

func TestAggregateInputsUsesAPIReaderForMetricPruning(t *testing.T) {
	g := NewWithT(t)
	ctx := context.Background()
	service := &servicesApi.DatabaseService{
		ObjectMeta: metav1.ObjectMeta{Name: servicesApi.DatabaseServiceInstanceName},
	}
	provider := &infraApi.DatabaseProvider{
		ObjectMeta: metav1.ObjectMeta{Name: "authoritative-provider"},
	}

	m := NewModule(&moduleconfig.Config{})
	m.apiReader = serviceTestClient(t, service, provider)
	rr := serviceTestRequest(t, serviceTestClient(t, service), service)

	dbmetrics.SetProviderReachable(provider.Name, true)
	g.Expect(m.aggregateInputs(ctx, rr)).To(Succeed())
	g.Expect(testutil.GatherAndCount(ctrlmetrics.Registry, "dbprovider_reachable")).To(Equal(1))

	dbmetrics.PruneProviderMetrics(nil)
}

func TestAggregateInputsClearsMetricsWhenProviderCRDIsMissing(t *testing.T) {
	g := NewWithT(t)
	ctx := context.Background()
	service := &servicesApi.DatabaseService{
		ObjectMeta: metav1.ObjectMeta{Name: servicesApi.DatabaseServiceInstanceName},
	}
	noMatch := &meta.NoKindMatchError{
		GroupKind: schema.GroupKind{
			Group: infraApi.GroupVersion.Group,
			Kind:  "DatabaseProvider",
		},
	}
	cachedClient := serviceTestClientWithListError(t, noMatch, service)

	m := NewModule(&moduleconfig.Config{})
	m.apiReader = serviceTestClient(t, service)
	rr := serviceTestRequest(t, cachedClient, service)

	dbmetrics.SetProviderReachable("removed-provider", true)
	g.Expect(m.aggregateInputs(ctx, rr)).To(Succeed())
	g.Expect(testutil.GatherAndCount(ctrlmetrics.Registry, "dbprovider_reachable")).To(Equal(0))
}

func TestAggregateInputsPrunesOnlyProvidersReturnedByAPIReader(t *testing.T) {
	g := NewWithT(t)
	ctx := context.Background()
	service := &servicesApi.DatabaseService{
		ObjectMeta: metav1.ObjectMeta{Name: servicesApi.DatabaseServiceInstanceName},
	}
	deletedProvider := &infraApi.DatabaseProvider{
		ObjectMeta: metav1.ObjectMeta{Name: "deleted-provider"},
	}
	currentProvider := &infraApi.DatabaseProvider{
		ObjectMeta: metav1.ObjectMeta{Name: "current-provider"},
	}

	m := NewModule(&moduleconfig.Config{})
	m.apiReader = serviceTestClient(t, service, currentProvider)
	rr := serviceTestRequest(t, serviceTestClient(t, service, deletedProvider), service)

	dbmetrics.SetProviderReachable(deletedProvider.Name, true)
	dbmetrics.SetProviderReachable(currentProvider.Name, true)
	g.Expect(m.aggregateInputs(ctx, rr)).To(Succeed())
	g.Expect(testutil.GatherAndCount(ctrlmetrics.Registry, "dbprovider_reachable")).To(Equal(1))

	dbmetrics.PruneProviderMetrics(nil)
}

func TestReportStatusRefreshesReleasesBeforeReturningAggregationError(t *testing.T) {
	g := NewWithT(t)
	ctx := context.Background()
	service := &servicesApi.DatabaseService{
		ObjectMeta: metav1.ObjectMeta{Name: servicesApi.DatabaseServiceInstanceName},
	}
	listErr := errors.New("list unavailable")
	apiReader := serviceTestClientWithListError(t, listErr, service)

	m := NewModule(&moduleconfig.Config{})
	m.apiReader = apiReader
	rr := serviceTestRequest(t, serviceTestClient(t, service), service)

	err := m.reportStatus(ctx, rr)
	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring("list unavailable"))
	g.Expect(service.Status.Releases).To(HaveLen(1))
	g.Expect(service.Status.Releases[0].Name).To(Equal(moduleconfig.ReleasePlatform))
}

func serviceTestClientWithListError(
	t *testing.T,
	listErr error,
	objects ...client.Object,
) client.Client {
	t.Helper()

	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{
		infraApi.AddToScheme,
		servicesApi.AddToScheme,
	} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}

	return fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&servicesApi.DatabaseService{}, &infraApi.DatabaseProvider{}).
		WithObjects(objects...).
		WithInterceptorFuncs(interceptor.Funcs{
			List: func(context.Context, client.WithWatch, client.ObjectList, ...client.ListOption) error {
				return listErr
			},
		}).
		Build()
}
