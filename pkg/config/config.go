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

package config

import (
	"fmt"
	"os"
	"time"

	"github.com/blang/semver/v4"
	"github.com/go-viper/mapstructure/v2"
	operatorversion "github.com/operator-framework/api/pkg/lib/version"
	"github.com/spf13/viper"

	common "github.com/opendatahub-io/odh-platform-utilities/api/common"
	fwapi "github.com/opendatahub-io/odh-platform-utilities/framework/api"
)

// PlatformVersion wraps semver.Version and implements encoding.TextUnmarshaler
// so mapstructure can decode the platformVersion ConfigMap key directly.
type PlatformVersion struct {
	semver.Version
}

// UnmarshalText implements encoding.TextUnmarshaler.
// An empty string decodes to the zero semver (0.0.0) without error.
func (v *PlatformVersion) UnmarshalText(text []byte) error {
	s := string(text)
	if s == "" {
		v.Version = semver.Version{}
		return nil
	}
	sv, err := semver.ParseTolerant(s)
	if err != nil {
		return fmt.Errorf("parsing platform version %q: %w", s, err)
	}
	v.Version = sv
	return nil
}

const (
	KeyOperatorNS      = "operator-namespace"
	KeyPlatformType    = "platformType"
	KeyPlatformVersion = "platformVersion"

	OperatorConfigMapName = "odh-databaseservice-config"

	KeyMetricsBindAddr    = "controller.metrics.bind-address"
	KeyHealthBindAddr     = "controller.health.bind-address"
	KeyLeaderElectEnabled = "controller.leader-election.enabled"
	KeyLeaderElectID      = "controller.leader-election.id"
	KeyZapLevel           = "controller.zap.level"
	KeyZapDevMode         = "controller.zap.dev-mode"
	KeyZapEncoder         = "controller.zap.encoder"
	KeyPprofEnabled       = "controller.pprof.enabled"
	KeyPprofBindAddr      = "controller.pprof.bind-address"

	// KeyDatabaseServiceRetryInterval is the periodic-retry interval for the
	// DatabaseService reconciler, passed to reconciler.WithDefaultRequeueAfter.
	KeyDatabaseServiceRetryInterval = "databaseservice.retry-interval"
	// KeyDatabaseProviderRetryInterval is the periodic-retry interval for the
	// DatabaseProvider reconciler.
	KeyDatabaseProviderRetryInterval = "databaseprovider.retry-interval"

	// PlatformType* are the identifier strings written to the platformType
	// ConfigMap key by the platform operator. They match the switch cases in
	// odhcluster.DetectPlatform and are distinct from the odhcluster.Platform
	// display-name constants ("Open Data Hub", "OpenShift AI Self-Managed", …).
	PlatformTypeOpenDataHub      = "OpenDataHub"
	PlatformTypeSelfManagedRhoai = "SelfManagedRhoai"
	PlatformTypeManagedRhoai     = "ManagedRhoai"

	DefaultOperatorNS      = "odh-db-operator-system"
	DefaultPlatformType    = PlatformTypeOpenDataHub
	DefaultPlatformVersion = ""

	DefaultMetricsBindAddr    = ":8080"
	DefaultHealthBindAddr     = ":8081"
	DefaultLeaderElectEnabled = true
	DefaultLeaderElectID      = "odh-db-operator-lock"
	DefaultZapLevel           = "info"
	DefaultZapDevMode         = false
	DefaultZapEncoder         = ""
	DefaultPprofEnabled       = false

	// DefaultRetryInterval is the compiled default for
	// KeyDatabaseServiceRetryInterval.
	DefaultRetryInterval = 3 * time.Minute

	TestedPostgresVersionMin = 130000
	TestedPostgresVersionMax = 179999

	// ReleasePlatform is the release name used in status.releases for the
	// platform version handshake.
	ReleasePlatform = "platform"

	// ConfigPathEnvVar is the environment variable that points to the mounted
	// ConfigMap directory (or a single config file).
	ConfigPathEnvVar = "ODH_MODULE_OPERATOR_CONFIGURATION_PATH"

	// EnvPrefix is the prefix for environment variables that override
	// configuration values (e.g. ODH_MODULE_OPERATOR_PLATFORM_TYPE).
	EnvPrefix = "ODH_MODULE_OPERATOR"
)

// Config holds the complete operator configuration.
//
// Values are loaded from (in order of precedence):
//  1. Struct field defaults
//  2. ConfigMap files (from ODH_MODULE_OPERATOR_CONFIGURATION_PATH)
//  3. Environment variables (ODH_MODULE_OPERATOR_ prefix)
//
// Controller-runtime fields use dot-separated ConfigMap keys under
// the "controller." prefix (e.g. "controller.leader-election.enabled").
//
// This config covers generic controller settings, Internal provider image
// defaults, and independent retry intervals for DatabaseService and
// DatabaseProvider reconciliation.
type Config struct {
	OperatorNamespace string           `mapstructure:"operator-namespace"`
	PlatformType      string           `mapstructure:"platformType"`
	PlatformVersion   PlatformVersion  `mapstructure:"platformVersion"`
	Controller        ControllerConfig `mapstructure:"controller"`
	Internal          InternalConfig   `mapstructure:"internal"`
	DatabaseProvider  RetryConfig      `mapstructure:"databaseprovider"`
	DatabaseService   RetryConfig      `mapstructure:"databaseservice"`
}

// RetryConfig holds one reconciler's periodic-retry interval, passed to
// reconciler.WithDefaultRequeueAfter.
type RetryConfig struct {
	RetryInterval time.Duration `mapstructure:"retry-interval"`
}

type InternalConfig struct {
	PostgresImage string `mapstructure:"postgres-image"`
	PgvectorImage string `mapstructure:"pgvector-image"`
}

type ControllerConfig struct {
	Metrics        MetricsConfig        `mapstructure:"metrics"`
	Health         HealthConfig         `mapstructure:"health"`
	LeaderElection LeaderElectionConfig `mapstructure:"leader-election"`
	Zap            ZapConfig            `mapstructure:"zap"`
	Pprof          PprofConfig          `mapstructure:"pprof"`
}

type MetricsConfig struct {
	BindAddress string `mapstructure:"bind-address"`
}

type HealthConfig struct {
	BindAddress string `mapstructure:"bind-address"`
}

type LeaderElectionConfig struct {
	Enabled bool   `mapstructure:"enabled"`
	ID      string `mapstructure:"id"`
}

type ZapConfig struct {
	Level   string `mapstructure:"level"`
	DevMode bool   `mapstructure:"dev-mode"`
	Encoder string `mapstructure:"encoder"`
}

type PprofConfig struct {
	Enabled     bool   `mapstructure:"enabled"`
	BindAddress string `mapstructure:"bind-address"`
}

// ComponentRelease returns the platform handshake entry for status.releases.
func (c *Config) ComponentRelease() common.ComponentRelease {
	return common.ComponentRelease{
		Name:    ReleasePlatform,
		Version: c.PlatformVersion.String(),
	}
}

// PlatformRelease returns the fwapi.Release used by the reconciler and stored
// in m.release. PlatformVersion is already parsed at load time — no error.
func (c *Config) PlatformRelease() fwapi.Release {
	return fwapi.Release{
		Name:    fwapi.Platform(c.PlatformType),
		Version: operatorversion.OperatorVersion{Version: c.PlatformVersion.Version},
	}
}

// NewViper returns a Viper instance primed with this module's compiled
// defaults. Callers can bind flags before loading so Cobra flags become the
// highest-precedence configuration source.
func NewViper() *viper.Viper {
	v := viper.New()
	setDefaults(v)
	return v
}

// Load reads operator configuration from all available sources.
//
// The loading sequence:
//  1. Set defaults
//  2. Read ConfigMap files from the explicitly configured filesystem, explicit
//     path, or ODH_MODULE_OPERATOR_CONFIGURATION_PATH
//  3. Bind environment variables with the ODH_MODULE_OPERATOR_ prefix
//  4. Unmarshal into the Config struct
func Load(opts ...Option) (*Config, error) {
	loadOptions := &LoadOptions{}
	for _, opt := range opts {
		if opt != nil {
			opt.applyOption(loadOptions)
		}
	}

	v := loadOptions.Viper
	if v == nil {
		v = NewViper()
	} else {
		setDefaults(v)
	}

	configFS := loadOptions.FS
	var configFile string
	if configFS == nil {
		configPath := loadOptions.ConfigPath
		if configPath == "" {
			configPath = os.Getenv(ConfigPathEnvVar)
		}
		if configPath != "" {
			info, err := os.Stat(configPath)
			if err != nil {
				return nil, fmt.Errorf("reading config path %s: %w", configPath, err)
			}
			if info.IsDir() {
				configFS = os.DirFS(configPath)
			} else {
				configFile = configPath
			}
		}
	}

	if configFS != nil {
		if err := loadFromFS(v, configFS); err != nil {
			return nil, fmt.Errorf("loading config from filesystem: %w", err)
		}
	}
	if configFile != "" {
		if err := loadFromFile(v, configFile); err != nil {
			return nil, fmt.Errorf("loading config from file: %w", err)
		}
	}

	if err := bindEnv(v); err != nil {
		return nil, fmt.Errorf("binding env vars: %w", err)
	}

	cfg := &Config{}
	if err := Decode(
		v,
		cfg,
		mapstructure.TextUnmarshallerHookFunc(),
		mapstructure.StringToSliceHookFunc(","),
	); err != nil {
		return nil, fmt.Errorf("unmarshaling config: %w", err)
	}

	return cfg, nil
}
