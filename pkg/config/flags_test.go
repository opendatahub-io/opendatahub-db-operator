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

package config_test

import (
	"testing"
	"testing/fstest"
	"time"

	. "github.com/onsi/gomega"
	"github.com/spf13/cobra"

	"github.com/opendatahub-io/opendatahub-db-operator/pkg/config"
)

func TestRegisterFlags_DerivesFlagNamesFromConfigStruct(t *testing.T) {
	g := NewWithT(t)

	cmd := &cobra.Command{Use: "test"}
	v := config.NewViper()

	g.Expect(config.RegisterFlags(cmd, v)).To(Succeed())

	for _, name := range []string{
		"operator-namespace",
		"platform-type",
		"platform-version",
		"controller-metrics-bind-address",
		"controller-leader-election-enabled",
		"controller-webhook-enabled",
		"controller-webhook-port",
		"controller-webhook-cert-dir",
		"databaseservice-retry-interval",
		"schemaclaim-retry-interval",
	} {
		g.Expect(cmd.Flags().Lookup(name)).NotTo(BeNil(), name)
	}
}

func TestRegisterFlags_WebhookDefaults(t *testing.T) {
	g := NewWithT(t)

	cmd := &cobra.Command{Use: "test"}
	g.Expect(config.RegisterFlags(cmd, config.NewViper())).To(Succeed())

	flagDefault := func(name string) string {
		flag := cmd.Flags().Lookup(name)
		g.Expect(flag).NotTo(BeNil(), name)
		if flag == nil {
			t.FailNow()
		}
		return flag.DefValue
	}
	g.Expect(flagDefault("controller-webhook-enabled")).To(Equal("true"))
	g.Expect(flagDefault("controller-webhook-port")).To(Equal("9443"))
	g.Expect(flagDefault("controller-webhook-cert-dir")).To(Equal("/tmp/k8s-webhook-server/serving-certs"))
}

func TestLoad_WebhookFlagsOverrideDefaults(t *testing.T) {
	g := NewWithT(t)

	cmd := &cobra.Command{Use: "test"}
	v := config.NewViper()
	g.Expect(config.RegisterFlags(cmd, v)).To(Succeed())
	g.Expect(cmd.ParseFlags([]string{
		"--controller-webhook-enabled=false",
		"--controller-webhook-port=9444",
		"--controller-webhook-cert-dir=/var/run/webhook/certs",
	})).To(Succeed())

	cfg, err := config.Load(config.LoadOptions{Viper: v})
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(cfg.Controller.Webhook.Enabled).To(BeFalse())
	g.Expect(cfg.Controller.Webhook.Port).To(Equal(9444))
	g.Expect(cfg.Controller.Webhook.CertDir).To(Equal("/var/run/webhook/certs"))
}

func TestLoad_FlagsOverrideEnvAndConfigMap(t *testing.T) {
	g := NewWithT(t)

	t.Setenv("ODH_MODULE_OPERATOR_PLATFORM_TYPE", config.PlatformTypeSelfManagedRhoai)

	cmd := &cobra.Command{Use: "test"}
	v := config.NewViper()
	g.Expect(config.RegisterFlags(cmd, v)).To(Succeed())
	g.Expect(cmd.ParseFlags([]string{
		"--platform-type", config.PlatformTypeManagedRhoai,
		"--platform-version", "2.1.0",
		"--controller-leader-election-enabled=false",
		"--databaseservice-retry-interval", "45s",
	})).To(Succeed())

	cfg, err := config.Load(
		config.LoadOptions{
			Viper: v,
			FS: fstest.MapFS{
				config.KeyPlatformType:                 {Data: []byte(config.DefaultPlatformType)},
				config.KeyDatabaseServiceRetryInterval: {Data: []byte("3m")},
			},
		},
	)
	g.Expect(err).NotTo(HaveOccurred())

	g.Expect(cfg.PlatformType).To(Equal(config.PlatformTypeManagedRhoai))
	g.Expect(cfg.PlatformVersion.String()).To(Equal("2.1.0"))
	g.Expect(cfg.Controller.LeaderElection.Enabled).To(BeFalse())
	g.Expect(cfg.DatabaseService.RetryInterval).To(Equal(45 * time.Second))
}
