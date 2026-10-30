package manager

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	. "github.com/onsi/gomega"
	"k8s.io/client-go/util/cert"
)

func TestLazyMetricsCertificateAdoptsMountedCertificate(t *testing.T) {
	g := NewWithT(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	certDir := t.TempDir()
	provider := newLazyMetricsCertificate(ctx, certDir)

	fallback, err := provider.GetCertificate(nil)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(fallback).NotTo(BeNil())

	certPEM, keyPEM, err := cert.GenerateSelfSignedCertKey(
		"odh-db-operator-metrics-service.test.svc",
		nil,
		nil,
	)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(os.WriteFile(filepath.Join(certDir, "tls.crt"), certPEM, 0o600)).To(Succeed())
	g.Expect(os.WriteFile(filepath.Join(certDir, "tls.key"), keyPEM, 0o600)).To(Succeed())

	mounted, err := provider.GetCertificate(nil)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(mounted).NotTo(BeNil())
	g.Expect(bytes.Equal(fallback.Certificate[0], mounted.Certificate[0])).To(BeFalse())
}

func TestLazyMetricsCertificateReloadsMountedCertificate(t *testing.T) {
	g := NewWithT(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	certDir := t.TempDir()
	certPEM, keyPEM, err := cert.GenerateSelfSignedCertKey("metrics.test.svc", nil, nil)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(os.WriteFile(filepath.Join(certDir, "tls.crt"), certPEM, 0o600)).To(Succeed())
	g.Expect(os.WriteFile(filepath.Join(certDir, "tls.key"), keyPEM, 0o600)).To(Succeed())

	provider := newLazyMetricsCertificate(ctx, certDir)
	initial, err := provider.GetCertificate(nil)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(initial).NotTo(BeNil())

	replacementPEM, replacementKey, err := cert.GenerateSelfSignedCertKey("metrics.test.svc", nil, nil)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(os.WriteFile(filepath.Join(certDir, "tls.crt"), replacementPEM, 0o600)).To(Succeed())
	g.Expect(os.WriteFile(filepath.Join(certDir, "tls.key"), replacementKey, 0o600)).To(Succeed())

	g.Eventually(func() bool {
		current, err := provider.GetCertificate(nil)
		return err == nil && current != nil && !bytes.Equal(initial.Certificate[0], current.Certificate[0])
	}, "15s", "100ms").Should(BeTrue())
}
