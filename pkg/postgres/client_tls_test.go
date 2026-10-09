/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package postgres

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	. "github.com/onsi/gomega"
	platformtls "github.com/opendatahub-io/odh-platform-utilities/pkg/tls"
	configv1 "github.com/openshift/api/config/v1"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

func TestClientNegotiatesTLSAccordingToProfileMinimum(t *testing.T) {
	g := NewWithT(t)
	certPath, keyPath := writePostgresTLSCertificate(t)
	configPath := filepath.Join(t.TempDir(), "postgres-ssl.conf")
	config := "listen_addresses = '*'\n" +
		"ssl = on\n" +
		"ssl_ca_file = '/tmp/testcontainers-go/postgres/ca_cert.pem'\n" +
		"ssl_cert_file = '/tmp/testcontainers-go/postgres/server.cert'\n" +
		"ssl_key_file = '/tmp/testcontainers-go/postgres/server.key'\n" +
		"ssl_min_protocol_version = 'TLSv1.2'\n" +
		"ssl_max_protocol_version = 'TLSv1.2'\n"
	g.Expect(os.WriteFile(configPath, []byte(config), 0o600)).To(Succeed())

	container, err := tcpostgres.Run(t.Context(), "postgres:16",
		tcpostgres.WithDatabase("testdb"),
		tcpostgres.WithUsername("testuser"),
		tcpostgres.WithPassword("testpassword"),
		tcpostgres.WithConfigFile(configPath),
		tcpostgres.WithSSLCert(certPath, certPath, keyPath),
		tcpostgres.BasicWaitStrategies(),
	)
	g.Expect(err).NotTo(HaveOccurred())
	t.Cleanup(func() {
		g.Expect(container.Terminate(context.Background())).To(Succeed())
	})

	connectionString, err := container.ConnectionString(t.Context(), "sslmode=require")
	g.Expect(err).NotTo(HaveOccurred())
	cfg, err := ConfigFromDSN(connectionString)
	g.Expect(err).NotTo(HaveOccurred())

	tls12Mutator, _ := platformtls.ConfigFromProfile(configv1.TLSProfileSpec{
		MinTLSVersion: configv1.VersionTLS12,
	})
	cfg.TLSConfigMutator = tls12Mutator
	client, err := NewClient(t.Context(), cfg)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(client.Ping(t.Context())).To(Succeed())
	var encrypted bool
	var version string
	row, err := client.QueryRow(t.Context(), "SELECT ssl, version FROM pg_stat_ssl WHERE pid = pg_backend_pid()")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(row.Scan(&encrypted, &version)).To(Succeed())
	g.Expect(encrypted).To(BeTrue())
	g.Expect(version).To(Equal("TLSv1.2"))
	client.Close()

	tls13Mutator, _ := platformtls.ConfigFromProfile(configv1.TLSProfileSpec{
		MinTLSVersion: configv1.VersionTLS13,
	})
	cfg.TLSConfigMutator = tls13Mutator
	client, err = NewClient(t.Context(), cfg)
	g.Expect(err).NotTo(HaveOccurred())
	t.Cleanup(client.Close)
	g.Expect(client.Ping(t.Context())).To(HaveOccurred())
}

func writePostgresTLSCertificate(t *testing.T) (string, string) {
	t.Helper()
	g := NewWithT(t)

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	g.Expect(err).NotTo(HaveOccurred())
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "localhost"},
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		DNSNames:              []string{"localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	g.Expect(err).NotTo(HaveOccurred())
	privateKey, err := x509.MarshalPKCS8PrivateKey(key)
	g.Expect(err).NotTo(HaveOccurred())

	dir := t.TempDir()
	certPath := filepath.Join(dir, "server.crt")
	keyPath := filepath.Join(dir, "server.key")
	g.Expect(os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600)).To(Succeed())
	privateKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateKey})
	g.Expect(os.WriteFile(keyPath, privateKeyPEM, 0o600)).To(Succeed())
	return certPath, keyPath
}
