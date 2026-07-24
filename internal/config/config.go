// Package config loads and validates typed application configuration from
// YAML with environment-variable overrides (STRATO_SECTION_KEY), so the same
// artifact runs in dev, compose, and Kubernetes with env-only changes.
package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/viper"
)

// Config is the root of all runtime configuration.
type Config struct {
	Server     Server     `mapstructure:"server"`
	Postgres   Postgres   `mapstructure:"postgres"`
	Redis      Redis      `mapstructure:"redis"`
	S3         S3         `mapstructure:"s3"`
	Auth       Auth       `mapstructure:"auth"`
	Encryption Encryption `mapstructure:"encryption"`
	Upload     Upload     `mapstructure:"upload"`
	Quota      Quota      `mapstructure:"quota"`
	RateLimit  RateLimit  `mapstructure:"rate_limit"`
	Worker     Worker     `mapstructure:"worker"`
	Telemetry  Telemetry  `mapstructure:"telemetry"`
	Log        Log        `mapstructure:"log"`
}

// Server holds listener configuration.
type Server struct {
	GRPCAddr        string        `mapstructure:"grpc_addr"`
	HTTPAddr        string        `mapstructure:"http_addr"`
	MetricsAddr     string        `mapstructure:"metrics_addr"`
	ShutdownTimeout time.Duration `mapstructure:"shutdown_timeout"`
	TLS             TLS           `mapstructure:"tls"`
}

// TLS enables direct TLS on listeners (usually terminated at the ingress).
type TLS struct {
	Enabled  bool   `mapstructure:"enabled"`
	CertFile string `mapstructure:"cert_file"`
	KeyFile  string `mapstructure:"key_file"`
}

// Postgres holds connection-pool settings.
type Postgres struct {
	Host            string        `mapstructure:"host"`
	Port            int           `mapstructure:"port"`
	User            string        `mapstructure:"user"`
	Password        string        `mapstructure:"password"`
	Database        string        `mapstructure:"database"`
	SSLMode         string        `mapstructure:"ssl_mode"`
	MaxConns        int32         `mapstructure:"max_conns"`
	MinConns        int32         `mapstructure:"min_conns"`
	MaxConnLifetime time.Duration `mapstructure:"max_conn_lifetime"`
}

// DSN renders the pgx connection string.
func (p Postgres) DSN() string {
	return fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=%s",
		p.User, p.Password, p.Host, p.Port, p.Database, p.SSLMode)
}

// Redis holds client settings.
type Redis struct {
	Addr     string `mapstructure:"addr"`
	Password string `mapstructure:"password"`
	DB       int    `mapstructure:"db"`
}

// S3 holds MinIO/S3 settings.
type S3 struct {
	Endpoint     string        `mapstructure:"endpoint"`
	AccessKey    string        `mapstructure:"access_key"`
	SecretKey    string        `mapstructure:"secret_key"`
	Bucket       string        `mapstructure:"bucket"`
	Region       string        `mapstructure:"region"`
	UseSSL       bool          `mapstructure:"use_ssl"`
	SignedURLTTL time.Duration `mapstructure:"signed_url_ttl"`
}

// Auth holds token settings.
type Auth struct {
	JWTSecret       string        `mapstructure:"jwt_secret"`
	Issuer          string        `mapstructure:"issuer"`
	AccessTokenTTL  time.Duration `mapstructure:"access_token_ttl"`
	RefreshTokenTTL time.Duration `mapstructure:"refresh_token_ttl"`
}

// Encryption holds the master KEK.
type Encryption struct {
	// MasterKey is base64 of 32 bytes. In production this arrives from a
	// secret manager via env var, never from a file in the repo.
	MasterKey string `mapstructure:"master_key"`
}

// MasterKeyBytes decodes and validates the KEK.
func (e Encryption) MasterKeyBytes() ([]byte, error) {
	key, err := base64.StdEncoding.DecodeString(e.MasterKey)
	if err != nil {
		return nil, fmt.Errorf("encryption.master_key is not valid base64: %w", err)
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("encryption.master_key must decode to 32 bytes, got %d", len(key))
	}
	return key, nil
}

// Upload holds chunking policy.
type Upload struct {
	ChunkSize         int64         `mapstructure:"chunk_size"`
	MaxFileSize       int64         `mapstructure:"max_file_size"`
	SessionTTL        time.Duration `mapstructure:"session_ttl"`
	MaxParallelChunks int           `mapstructure:"max_parallel_chunks"`
}

// Quota holds default per-user storage limits.
type Quota struct {
	DefaultBytes int64 `mapstructure:"default_bytes"`
}

// RateLimit holds request throttling policy.
type RateLimit struct {
	Enabled               bool `mapstructure:"enabled"`
	RequestsPerMinute     int  `mapstructure:"requests_per_minute"`
	AuthRequestsPerMinute int  `mapstructure:"auth_requests_per_minute"`
}

// Worker holds background job settings.
type Worker struct {
	GCInterval time.Duration `mapstructure:"gc_interval"`
	PoolSize   int           `mapstructure:"pool_size"`
	// TrashRetention is how long soft-deleted files remain restorable
	// before the GC worker purges them permanently.
	TrashRetention time.Duration `mapstructure:"trash_retention"`
	// OrphanGrace protects zero-ref blobs from GC while an upload that just
	// created them is still attaching a version.
	OrphanGrace time.Duration `mapstructure:"orphan_grace"`
}

// Telemetry holds tracing/metrics settings.
type Telemetry struct {
	ServiceName  string  `mapstructure:"service_name"`
	OTLPEndpoint string  `mapstructure:"otlp_endpoint"`
	SampleRatio  float64 `mapstructure:"sample_ratio"`
}

// Log holds logger settings.
type Log struct {
	Level  string `mapstructure:"level"`
	Format string `mapstructure:"format"`
}

// Load reads the YAML file at path (empty string skips the file and relies
// on env + defaults), applies STRATO_* env overrides, and validates.
func Load(path string) (*Config, error) {
	v := viper.New()
	v.SetEnvPrefix("STRATO")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	if path != "" {
		v.SetConfigFile(path)
		if err := v.ReadInConfig(); err != nil {
			return nil, fmt.Errorf("reading config %s: %w", path, err)
		}
	}

	// Viper's AutomaticEnv only resolves keys it has seen; binding
	// explicitly makes env-only deployment (no YAML) work.
	for _, key := range allKeys() {
		if err := v.BindEnv(key); err != nil {
			return nil, fmt.Errorf("binding env for %s: %w", key, err)
		}
	}

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("unmarshaling config: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// Validate enforces invariants that would otherwise fail obscurely at runtime.
func (c *Config) Validate() error {
	var errs []error
	if c.Auth.JWTSecret == "" {
		errs = append(errs, errors.New("auth.jwt_secret is required"))
	}
	if len(c.Auth.JWTSecret) < 32 {
		errs = append(errs, errors.New("auth.jwt_secret must be at least 32 characters"))
	}
	if _, err := c.Encryption.MasterKeyBytes(); err != nil {
		errs = append(errs, err)
	}
	if c.Upload.ChunkSize < 1<<20 {
		errs = append(errs, errors.New("upload.chunk_size must be at least 1 MiB"))
	}
	if c.Server.TLS.Enabled && (c.Server.TLS.CertFile == "" || c.Server.TLS.KeyFile == "") {
		errs = append(errs, errors.New("server.tls requires cert_file and key_file when enabled"))
	}
	return errors.Join(errs...)
}

func allKeys() []string {
	return []string{
		"server.grpc_addr", "server.http_addr", "server.metrics_addr", "server.shutdown_timeout",
		"postgres.host", "postgres.port", "postgres.user", "postgres.password", "postgres.database",
		"postgres.ssl_mode", "postgres.max_conns", "postgres.min_conns", "postgres.max_conn_lifetime",
		"redis.addr", "redis.password", "redis.db",
		"s3.endpoint", "s3.access_key", "s3.secret_key", "s3.bucket", "s3.region", "s3.use_ssl", "s3.signed_url_ttl",
		"auth.jwt_secret", "auth.issuer", "auth.access_token_ttl", "auth.refresh_token_ttl",
		"encryption.master_key",
		"upload.chunk_size", "upload.max_file_size", "upload.session_ttl", "upload.max_parallel_chunks",
		"quota.default_bytes",
		"rate_limit.enabled", "rate_limit.requests_per_minute", "rate_limit.auth_requests_per_minute",
		"worker.gc_interval", "worker.pool_size", "worker.trash_retention", "worker.orphan_grace",
		"telemetry.service_name", "telemetry.otlp_endpoint", "telemetry.sample_ratio",
		"log.level", "log.format",
	}
}
