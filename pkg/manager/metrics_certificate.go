package manager

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"

	"k8s.io/client-go/util/cert"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/certwatcher"
)

const metricsCertificateDir = "/tmp/k8s-metrics-server/serving-certs"

type lazyMetricsCertificate struct {
	ctx      context.Context
	certPath string
	keyPath  string

	mutex    sync.Mutex
	watcher  *certwatcher.CertWatcher
	fallback *tls.Certificate
}

func newLazyMetricsCertificate(ctx context.Context, certDir string) *lazyMetricsCertificate {
	return &lazyMetricsCertificate{
		ctx:      ctx,
		certPath: filepath.Join(certDir, "tls.crt"),
		keyPath:  filepath.Join(certDir, "tls.key"),
	}
}

func (c *lazyMetricsCertificate) GetCertificate(
	_ *tls.ClientHelloInfo,
) (*tls.Certificate, error) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	if c.watcher != nil {
		return c.watcher.GetCertificate(nil)
	}

	_, certErr := os.Stat(c.certPath)
	_, keyErr := os.Stat(c.keyPath)
	if !os.IsNotExist(certErr) && !os.IsNotExist(keyErr) {
		watcher, err := certwatcher.New(c.certPath, c.keyPath)
		if err != nil {
			return nil, fmt.Errorf("creating metrics certificate watcher: %w", err)
		}
		c.watcher = watcher
		go func() {
			if err := watcher.Start(c.ctx); err != nil && c.ctx.Err() == nil {
				ctrl.Log.Error(err, "metrics certificate watcher stopped")
			}
		}()
		return watcher.GetCertificate(nil)
	}

	if c.fallback == nil {
		certPEM, keyPEM, err := cert.GenerateSelfSignedCertKey(
			"localhost",
			[]net.IP{{127, 0, 0, 1}},
			nil,
		)
		if err != nil {
			return nil, fmt.Errorf("generating fallback metrics certificate: %w", err)
		}
		keyPair, err := tls.X509KeyPair(certPEM, keyPEM)
		if err != nil {
			return nil, fmt.Errorf("parsing fallback metrics certificate: %w", err)
		}
		c.fallback = &keyPair
	}

	return c.fallback, nil
}
