package controller_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	infraApi "github.com/opendatahub-io/opendatahub-db-operator/api/infrastructure/v1alpha1"
	moduleconfig "github.com/opendatahub-io/opendatahub-db-operator/pkg/config"
	"github.com/opendatahub-io/opendatahub-db-operator/pkg/controller"
	"github.com/opendatahub-io/opendatahub-db-operator/pkg/controller/claimerrors"
	"github.com/opendatahub-io/opendatahub-db-operator/pkg/postgres"
)

func TestNewClient_LoadsProviderConfigAndUsesFactory(t *testing.T) {
	g := NewWithT(t)

	provider := &infraApi.DatabaseProvider{
		ObjectMeta: metav1.ObjectMeta{Name: "sample-external"},
		Spec: infraApi.DatabaseProviderSpec{
			Type: infraApi.ProviderTypeExternal,
			External: &infraApi.ExternalProviderSpec{
				ConnectionSecretRef: corev1.SecretReference{
					Namespace: "db-admin",
					Name:      "external-admin",
				},
			},
		},
	}

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "external-admin",
			Namespace: "db-admin",
		},
		Data: map[string][]byte{
			postgres.SecretKeyHost:     []byte("postgres.db-admin.svc"),
			postgres.SecretKeyPort:     []byte("5432"),
			postgres.SecretKeyUser:     []byte("postgres"),
			postgres.SecretKeyPassword: []byte("secret"),
			postgres.SecretKeyDatabase: []byte("postgres"),
		},
	}

	cli := fake.NewClientBuilder().
		WithScheme(providerConfigScheme()).
		WithObjects(secret).
		Build()

	var gotConfig postgres.Config
	client, cfg, err := controller.NewClient(
		context.Background(),
		cli,
		provider,
		&moduleconfig.Config{OperatorNamespace: "odh-db-operator-system"},
		func(_ context.Context, cfg postgres.Config) (postgres.Client, error) {
			gotConfig = cfg
			return stubClient{config: cfg}, nil
		},
	)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(cfg).To(Equal(postgres.Config{
		Host:     "postgres.db-admin.svc",
		Port:     postgres.DefaultPort,
		User:     "postgres",
		Password: "secret",
		DBName:   "postgres",
		SSLMode:  postgres.SSLModeRequire,
	}))
	g.Expect(gotConfig).To(Equal(cfg))
	g.Expect(client.Config()).To(Equal(cfg))
}

type stubClient struct {
	config postgres.Config
}

func (s stubClient) Config() postgres.Config  { return s.config }
func (stubClient) Close()                     {}
func (stubClient) Ping(context.Context) error { return nil }
func (stubClient) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}
func (stubClient) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, nil
}
func (stubClient) QueryRow(context.Context, string, ...any) (pgx.Row, error) {
	return nil, nil
}

func TestEnsureClaimExtensions_EmptyNoop(t *testing.T) {
	g := NewWithT(t)
	cli := &recordingClient{installedByName: map[string]bool{"vector": true}}
	provider := &infraApi.DatabaseProvider{
		Spec: infraApi.DatabaseProviderSpec{Type: infraApi.ProviderTypeInternal},
	}
	g.Expect(controller.EnsureClaimExtensions(context.Background(), cli, provider, nil)).To(Succeed())
	g.Expect(cli.execSQLs).To(BeEmpty())
}

func TestEnsureClaimExtensions_InternalInstalls(t *testing.T) {
	g := NewWithT(t)
	cli := &recordingClient{}
	provider := &infraApi.DatabaseProvider{
		Spec: infraApi.DatabaseProviderSpec{Type: infraApi.ProviderTypeInternal},
	}
	g.Expect(controller.EnsureClaimExtensions(context.Background(), cli, provider, []string{"vector"})).To(Succeed())
	g.Expect(cli.execSQLs).To(HaveLen(1))
	g.Expect(strings.ToUpper(cli.execSQLs[0])).To(ContainSubstring("CREATE EXTENSION"))
	g.Expect(cli.execSQLs[0]).To(ContainSubstring("vector"))
}

func TestEnsureClaimExtensions_ExternalInstalledNoCreate(t *testing.T) {
	g := NewWithT(t)
	cli := &recordingClient{installedByName: map[string]bool{"vector": true}}
	provider := &infraApi.DatabaseProvider{
		Spec: infraApi.DatabaseProviderSpec{Type: infraApi.ProviderTypeExternal},
	}
	g.Expect(controller.EnsureClaimExtensions(context.Background(), cli, provider, []string{"vector"})).To(Succeed())
	g.Expect(cli.execSQLs).To(BeEmpty())
	for _, sql := range cli.execSQLs {
		g.Expect(strings.ToUpper(sql)).NotTo(ContainSubstring("CREATE EXTENSION"))
	}
}

func TestEnsureClaimExtensions_ExternalMissingNamedError(t *testing.T) {
	g := NewWithT(t)
	cli := &recordingClient{installedByName: map[string]bool{"vector": false}}
	provider := &infraApi.DatabaseProvider{
		Spec: infraApi.DatabaseProviderSpec{Type: infraApi.ProviderTypeExternal},
	}
	err := controller.EnsureClaimExtensions(context.Background(), cli, provider, []string{"vector"})
	var missing claimerrors.ExtensionNotAvailable
	g.Expect(errors.As(err, &missing)).To(BeTrue())
	g.Expect(missing.Extensions).To(Equal([]string{"vector"}))
	g.Expect(cli.execSQLs).To(BeEmpty())
	for _, sql := range cli.execSQLs {
		g.Expect(strings.ToUpper(sql)).NotTo(ContainSubstring("CREATE EXTENSION"))
	}
}

func TestEnsureClaimExtensions_ExternalMultipleMissingReportedTogether(t *testing.T) {
	g := NewWithT(t)
	cli := &recordingClient{installedByName: map[string]bool{"vector": false, "postgis": false}}
	provider := &infraApi.DatabaseProvider{
		Spec: infraApi.DatabaseProviderSpec{Type: infraApi.ProviderTypeExternal},
	}
	err := controller.EnsureClaimExtensions(context.Background(), cli, provider, []string{"vector", "postgis"})
	var missing claimerrors.ExtensionNotAvailable
	g.Expect(errors.As(err, &missing)).To(BeTrue())
	g.Expect(missing.Extensions).To(Equal([]string{"vector", "postgis"}))
	g.Expect(err).To(MatchError(ContainSubstring("vector, postgis")))
}

func TestEnsureClaimExtensions_NilProviderRejected(t *testing.T) {
	g := NewWithT(t)
	cli := &recordingClient{}
	err := controller.EnsureClaimExtensions(context.Background(), cli, nil, []string{"vector"})
	g.Expect(err).To(MatchError("provider is required for extension verification"))
}

type recordingClient struct {
	installedByName map[string]bool
	execSQLs        []string
}

func (c *recordingClient) Config() postgres.Config { return postgres.Config{} }
func (c *recordingClient) Close()                  {}
func (c *recordingClient) Ping(context.Context) error {
	return nil
}
func (c *recordingClient) Exec(_ context.Context, sql string, _ ...any) (pgconn.CommandTag, error) {
	c.execSQLs = append(c.execSQLs, sql)
	return pgconn.CommandTag{}, nil
}
func (c *recordingClient) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, nil
}
func (c *recordingClient) QueryRow(_ context.Context, _ string, args ...any) (pgx.Row, error) {
	if len(args) > 0 {
		if names, ok := args[0].([]string); ok {
			var installed []string
			for _, name := range names {
				if c.installedByName[name] {
					installed = append(installed, name)
				}
			}
			return recordingNamesRow{names: installed}, nil
		}
	}
	installed := false
	if c.installedByName != nil && len(args) > 0 {
		if name, ok := args[0].(string); ok {
			installed = c.installedByName[name]
		}
	}
	return recordingBoolRow{installed: installed}, nil
}

type recordingBoolRow struct {
	installed bool
}

func (r recordingBoolRow) Scan(dest ...any) error {
	if len(dest) != 1 {
		return errors.New("expected one scan destination")
	}
	ptr, ok := dest[0].(*bool)
	if !ok {
		return errors.New("expected *bool scan destination")
	}
	*ptr = r.installed
	return nil
}

type recordingNamesRow struct {
	names []string
}

func (r recordingNamesRow) Scan(dest ...any) error {
	if len(dest) != 1 {
		return errors.New("expected one scan destination")
	}
	ptr, ok := dest[0].(*[]string)
	if !ok {
		return errors.New("expected *[]string scan destination")
	}
	*ptr = append([]string(nil), r.names...)
	return nil
}
