package metrics

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	providerMutex sync.Mutex
	providerNames = map[string]struct{}{}

	providerReachable = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "dbprovider_reachable",
			Help: "Whether a DatabaseProvider is reachable",
		},
		[]string{"provider"},
	)

	providerServerVersion = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "dbprovider_server_version_info",
			Help: "PostgreSQL server version reported by a DatabaseProvider",
		},
		[]string{"provider", "version"},
	)
)

func init() {
	ctrlmetrics.Registry.MustRegister(providerReachable, providerServerVersion)
}

func SetProviderReachable(provider string, reachable bool) {
	providerMutex.Lock()
	defer providerMutex.Unlock()

	providerNames[provider] = struct{}{}
	value := 0.0
	if reachable {
		value = 1.0
	}
	providerReachable.WithLabelValues(provider).Set(value)
}

func SetProviderServerVersion(provider, version string) {
	providerMutex.Lock()
	defer providerMutex.Unlock()

	providerNames[provider] = struct{}{}
	providerServerVersion.DeletePartialMatch(prometheus.Labels{"provider": provider})
	providerServerVersion.WithLabelValues(provider, version).Set(1)
}

func ClearProviderServerVersion(provider string) {
	providerMutex.Lock()
	defer providerMutex.Unlock()

	providerServerVersion.DeletePartialMatch(prometheus.Labels{"provider": provider})
}

func PruneProviderMetrics(currentProviders []string) {
	current := make(map[string]struct{}, len(currentProviders))
	for _, provider := range currentProviders {
		current[provider] = struct{}{}
	}

	providerMutex.Lock()
	defer providerMutex.Unlock()

	for provider := range providerNames {
		if _, exists := current[provider]; exists {
			continue
		}

		delete(providerNames, provider)
		providerReachable.DeleteLabelValues(provider)
		providerServerVersion.DeletePartialMatch(prometheus.Labels{"provider": provider})
	}
}
