package metrics

import (
	"testing"

	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestProviderMetrics(t *testing.T) {
	t.Run("updates reachability", func(t *testing.T) {
		g := NewWithT(t)

		providerReachable.Reset()
		SetProviderReachable("metrics-test", true)
		g.Expect(testutil.ToFloat64(providerReachable.WithLabelValues("metrics-test"))).To(Equal(1.0))

		SetProviderReachable("metrics-test", false)
		g.Expect(testutil.ToFloat64(providerReachable.WithLabelValues("metrics-test"))).To(Equal(0.0))
	})

	t.Run("replaces the previous server version", func(t *testing.T) {
		g := NewWithT(t)

		providerServerVersion.Reset()
		SetProviderServerVersion("metrics-test", "16.6")
		SetProviderServerVersion("metrics-test", "17.2")

		g.Expect(testutil.CollectAndCount(providerServerVersion)).To(Equal(1))
		g.Expect(testutil.ToFloat64(providerServerVersion.WithLabelValues("metrics-test", "17.2"))).To(Equal(1.0))
	})

	t.Run("clears the server version", func(t *testing.T) {
		g := NewWithT(t)

		providerServerVersion.Reset()
		SetProviderServerVersion("metrics-clear-test", "16.6")
		ClearProviderServerVersion("metrics-clear-test")

		g.Expect(testutil.CollectAndCount(providerServerVersion)).To(Equal(0))
	})

	t.Run("prunes metrics for deleted providers", func(t *testing.T) {
		g := NewWithT(t)

		providerReachable.Reset()
		providerServerVersion.Reset()
		SetProviderReachable("metrics-removed", false)
		SetProviderServerVersion("metrics-removed", "16.6")
		SetProviderReachable("metrics-current", true)

		PruneProviderMetrics([]string{"metrics-current"})

		g.Expect(testutil.CollectAndCount(providerReachable)).To(Equal(1))
		g.Expect(testutil.ToFloat64(providerReachable.WithLabelValues("metrics-current"))).To(Equal(1.0))
		g.Expect(testutil.CollectAndCount(providerServerVersion)).To(Equal(0))
	})
}
