// Package config loads, saves, and provides defaults for agent-gateway's
// HCL configuration file.
package config

import (
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	"github.com/hashicorp/hcl/v2/hclparse"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/atomicfile"
)

//go:embed default.hcl
var defaultHCL []byte

// ---------------------------------------------------------------------------
// Public typed config structs
// ---------------------------------------------------------------------------

// Config is the typed representation of config.hcl.
type Config struct {
	Proxy         ProxyConfig         `hcl:"proxy,block"`
	Dashboard     DashboardConfig     `hcl:"dashboard,block"`
	Rules         RulesConfig         `hcl:"rules,block"`
	Secrets       SecretsConfig       `hcl:"secrets,block"`
	Audit         AuditConfig         `hcl:"audit,block"`
	Approval      ApprovalConfig      `hcl:"approval,block"`
	ProxyBehavior ProxyBehaviorConfig `hcl:"proxy_behavior,block"`
	Timeouts      TimeoutsConfig      `hcl:"timeouts,block"`
	Log           LogConfig           `hcl:"log,block"`
}

// ProxyConfig holds proxy listener settings.
type ProxyConfig struct {
	Listen string `hcl:"listen"`
}

// DashboardConfig holds dashboard listener settings.
type DashboardConfig struct {
	Listen      string `hcl:"listen"`
	OpenBrowser bool   `hcl:"open_browser"`
}

// RulesConfig holds rules directory path.
type RulesConfig struct {
	Dir string `hcl:"dir"`
}

// SecretsConfig holds secrets cache settings.
type SecretsConfig struct {
	CacheTTL time.Duration
}

// AuditConfig holds audit retention settings.
type AuditConfig struct {
	RetentionDays int    `hcl:"retention_days"`
	PruneAt       string `hcl:"prune_at"`
}

// ApprovalConfig holds approval flow settings.
type ApprovalConfig struct {
	Timeout    time.Duration
	MaxPending int `hcl:"max_pending"`
}

// ProxyBehaviorConfig holds proxy behaviour tunables.
type ProxyBehaviorConfig struct {
	NoInterceptHosts []string `hcl:"no_intercept_hosts"`
	MaxBodyBuffer    int64
}

// TimeoutsConfig holds all timeout durations.
type TimeoutsConfig struct {
	ConnectReadHeader      time.Duration
	MITMHandshake          time.Duration
	IdleKeepalive          time.Duration
	UpstreamDial           time.Duration
	UpstreamTLS            time.Duration
	UpstreamResponseHeader time.Duration
	UpstreamIdleKeepalive  time.Duration
	BodyBufferRead         time.Duration
	RequestBodyRead        time.Duration
	ResponseBodyRead       time.Duration
}

// LogConfig holds logging settings.
type LogConfig struct {
	Level  string `hcl:"level"`
	Format string `hcl:"format"`
}

// ---------------------------------------------------------------------------
// Wire structs — durations and sizes are decoded as strings then converted
// ---------------------------------------------------------------------------

type wireConfig struct {
	Proxy         wireProxy         `hcl:"proxy,block"`
	Dashboard     wireDashboard     `hcl:"dashboard,block"`
	Rules         wireRules         `hcl:"rules,block"`
	Secrets       wireSecrets       `hcl:"secrets,block"`
	Audit         wireAudit         `hcl:"audit,block"`
	Approval      wireApproval      `hcl:"approval,block"`
	ProxyBehavior wireProxyBehavior `hcl:"proxy_behavior,block"`
	Timeouts      wireTimeouts      `hcl:"timeouts,block"`
	Log           wireLog           `hcl:"log,block"`
}

type wireProxy struct {
	Listen string `hcl:"listen"`
}

type wireDashboard struct {
	Listen      string `hcl:"listen"`
	OpenBrowser *bool  `hcl:"open_browser"`
}

type wireRules struct {
	Dir string `hcl:"dir"`
}

type wireSecrets struct {
	CacheTTL string `hcl:"cache_ttl"`
}

type wireAudit struct {
	RetentionDays int    `hcl:"retention_days"`
	PruneAt       string `hcl:"prune_at"`
}

type wireApproval struct {
	Timeout    string `hcl:"timeout"`
	MaxPending int    `hcl:"max_pending"`
}

type wireProxyBehavior struct {
	NoInterceptHosts []string `hcl:"no_intercept_hosts"`
	MaxBodyBuffer    string   `hcl:"max_body_buffer"`
}

type wireTimeouts struct {
	ConnectReadHeader      string `hcl:"connect_read_header"`
	MITMHandshake          string `hcl:"mitm_handshake"`
	IdleKeepalive          string `hcl:"idle_keepalive"`
	UpstreamDial           string `hcl:"upstream_dial"`
	UpstreamTLS            string `hcl:"upstream_tls"`
	UpstreamResponseHeader string `hcl:"upstream_response_header"`
	UpstreamIdleKeepalive  string `hcl:"upstream_idle_keepalive"`
	BodyBufferRead         string `hcl:"body_buffer_read"`
	RequestBodyRead        string `hcl:"request_body_read"`
	ResponseBodyRead       string `hcl:"response_body_read"`
}

type wireLog struct {
	Level  string `hcl:"level"`
	Format string `hcl:"format"`
}

// ---------------------------------------------------------------------------
// Size parser
// ---------------------------------------------------------------------------

// parseSize parses a size string into bytes. Accepted formats:
//   - "1MiB", "500KiB", "4GiB", "1KiB"
//   - plain integer string (bytes)
func parseSize(s string) (int64, error) {
	suffixes := []struct {
		suffix string
		mult   int64
	}{
		{"GiB", 1 << 30},
		{"MiB", 1 << 20},
		{"KiB", 1 << 10},
	}
	for _, sf := range suffixes {
		if strings.HasSuffix(s, sf.suffix) {
			n, err := strconv.ParseInt(strings.TrimSuffix(s, sf.suffix), 10, 64)
			if err != nil {
				return 0, fmt.Errorf("invalid size %q: %w", s, err)
			}
			return n * sf.mult, nil
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid size %q: %w", s, err)
	}
	return n, nil
}

// parseDur parses a duration string, treating "0" and "0s" as zero.
func parseDur(s string) (time.Duration, error) {
	if s == "0" {
		return 0, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q: %w", s, err)
	}
	return d, nil
}

// ---------------------------------------------------------------------------
// Conversion: wire → typed Config
// ---------------------------------------------------------------------------

func fromWire(w wireConfig) (Config, error) {
	cacheTTL, err := parseDur(w.Secrets.CacheTTL)
	if err != nil {
		return Config{}, fmt.Errorf("secrets.cache_ttl: %w", err)
	}
	approvalTimeout, err := parseDur(w.Approval.Timeout)
	if err != nil {
		return Config{}, fmt.Errorf("approval.timeout: %w", err)
	}
	maxBodyBuf, err := parseSize(w.ProxyBehavior.MaxBodyBuffer)
	if err != nil {
		return Config{}, fmt.Errorf("proxy_behavior.max_body_buffer: %w", err)
	}
	connectReadHeader, err := parseDur(w.Timeouts.ConnectReadHeader)
	if err != nil {
		return Config{}, fmt.Errorf("timeouts.connect_read_header: %w", err)
	}
	mitmHandshake, err := parseDur(w.Timeouts.MITMHandshake)
	if err != nil {
		return Config{}, fmt.Errorf("timeouts.mitm_handshake: %w", err)
	}
	idleKeepalive, err := parseDur(w.Timeouts.IdleKeepalive)
	if err != nil {
		return Config{}, fmt.Errorf("timeouts.idle_keepalive: %w", err)
	}
	upstreamDial, err := parseDur(w.Timeouts.UpstreamDial)
	if err != nil {
		return Config{}, fmt.Errorf("timeouts.upstream_dial: %w", err)
	}
	upstreamTLS, err := parseDur(w.Timeouts.UpstreamTLS)
	if err != nil {
		return Config{}, fmt.Errorf("timeouts.upstream_tls: %w", err)
	}
	upstreamRespHdr, err := parseDur(w.Timeouts.UpstreamResponseHeader)
	if err != nil {
		return Config{}, fmt.Errorf("timeouts.upstream_response_header: %w", err)
	}
	upstreamIdleKA, err := parseDur(w.Timeouts.UpstreamIdleKeepalive)
	if err != nil {
		return Config{}, fmt.Errorf("timeouts.upstream_idle_keepalive: %w", err)
	}
	bodyBufRead, err := parseDur(w.Timeouts.BodyBufferRead)
	if err != nil {
		return Config{}, fmt.Errorf("timeouts.body_buffer_read: %w", err)
	}
	reqBodyRead, err := parseDur(w.Timeouts.RequestBodyRead)
	if err != nil {
		return Config{}, fmt.Errorf("timeouts.request_body_read: %w", err)
	}
	respBodyRead, err := parseDur(w.Timeouts.ResponseBodyRead)
	if err != nil {
		return Config{}, fmt.Errorf("timeouts.response_body_read: %w", err)
	}

	return Config{
		Proxy: ProxyConfig{
			Listen: w.Proxy.Listen,
		},
		Dashboard: DashboardConfig{
			Listen:      w.Dashboard.Listen,
			OpenBrowser: w.Dashboard.OpenBrowser != nil && *w.Dashboard.OpenBrowser,
		},
		Rules: RulesConfig{
			Dir: w.Rules.Dir,
		},
		Secrets: SecretsConfig{
			CacheTTL: cacheTTL,
		},
		Audit: AuditConfig{
			RetentionDays: w.Audit.RetentionDays,
			PruneAt:       w.Audit.PruneAt,
		},
		Approval: ApprovalConfig{
			Timeout:    approvalTimeout,
			MaxPending: w.Approval.MaxPending,
		},
		ProxyBehavior: ProxyBehaviorConfig{
			NoInterceptHosts: w.ProxyBehavior.NoInterceptHosts,
			MaxBodyBuffer:    maxBodyBuf,
		},
		Timeouts: TimeoutsConfig{
			ConnectReadHeader:      connectReadHeader,
			MITMHandshake:          mitmHandshake,
			IdleKeepalive:          idleKeepalive,
			UpstreamDial:           upstreamDial,
			UpstreamTLS:            upstreamTLS,
			UpstreamResponseHeader: upstreamRespHdr,
			UpstreamIdleKeepalive:  upstreamIdleKA,
			BodyBufferRead:         bodyBufRead,
			RequestBodyRead:        reqBodyRead,
			ResponseBodyRead:       respBodyRead,
		},
		Log: LogConfig{
			Level:  w.Log.Level,
			Format: w.Log.Format,
		},
	}, nil
}

// ---------------------------------------------------------------------------
// HCL parsing
// ---------------------------------------------------------------------------

// blockNames lists every top-level block type in config.hcl.
var blockNames = []string{
	"proxy", "dashboard", "rules", "secrets", "audit",
	"approval", "proxy_behavior", "timeouts", "log",
}

// parseHCL parses src and returns a wireConfig. Blocks absent from src are
// left at their zero values so the caller can merge with defaults.
func parseHCL(src []byte, filename string) (wireConfig, error) {
	parser := hclparse.NewParser()
	f, diags := parser.ParseHCL(src, filename)
	if diags.HasErrors() {
		return wireConfig{}, errors.New(diags.Error())
	}

	// Build a schema that accepts all known block types.
	schema := &hcl.BodySchema{}
	for _, name := range blockNames {
		schema.Blocks = append(schema.Blocks, hcl.BlockHeaderSchema{Type: name})
	}

	content, _, diags := f.Body.PartialContent(schema)
	if diags.HasErrors() {
		return wireConfig{}, errors.New(diags.Error())
	}

	var w wireConfig
	for _, block := range content.Blocks {
		var bd hcl.Diagnostics
		switch block.Type {
		case "proxy":
			bd = gohcl.DecodeBody(block.Body, nil, &w.Proxy)
		case "dashboard":
			bd = gohcl.DecodeBody(block.Body, nil, &w.Dashboard)
		case "rules":
			bd = gohcl.DecodeBody(block.Body, nil, &w.Rules)
		case "secrets":
			bd = gohcl.DecodeBody(block.Body, nil, &w.Secrets)
		case "audit":
			bd = gohcl.DecodeBody(block.Body, nil, &w.Audit)
		case "approval":
			bd = gohcl.DecodeBody(block.Body, nil, &w.Approval)
		case "proxy_behavior":
			bd = gohcl.DecodeBody(block.Body, nil, &w.ProxyBehavior)
		case "timeouts":
			bd = gohcl.DecodeBody(block.Body, nil, &w.Timeouts)
		case "log":
			bd = gohcl.DecodeBody(block.Body, nil, &w.Log)
		}
		if bd.HasErrors() {
			return wireConfig{}, errors.New(bd.Error())
		}
	}
	return w, nil
}

// ---------------------------------------------------------------------------
// DefaultConfig
// ---------------------------------------------------------------------------

// DefaultConfig returns the compiled-in default configuration.
func DefaultConfig() Config {
	w, err := parseHCL(defaultHCL, "default.hcl")
	if err != nil {
		// default.hcl is embedded and must always be valid.
		panic(fmt.Sprintf("config: invalid embedded default.hcl: %v", err))
	}
	cfg, err := fromWire(w)
	if err != nil {
		panic(fmt.Sprintf("config: invalid embedded default.hcl values: %v", err))
	}
	return cfg
}

// ---------------------------------------------------------------------------
// Save
// ---------------------------------------------------------------------------

// Save writes cfg to path at 0600, creating parent directories at 0700. The
// config is validated before any disk I/O so an invalid config never reaches
// disk via CLI paths. Normalization warnings raised during validation are
// discarded; they would already have surfaced on the preceding Load.
func Save(cfg Config, path string) error {
	if _, err := validateConfig(&cfg); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("config: mkdir %s: %w", filepath.Dir(path), err)
	}
	content := renderHCL(cfg)
	if err := atomicfile.Write(path, []byte(content), 0o600); err != nil {
		return fmt.Errorf("config: write %s: %w", path, err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Load
// ---------------------------------------------------------------------------

// Load reads the config from path. If the file does not exist it writes the
// defaults and returns them. Parse errors are returned as non-nil errors.
//
// The returned []string carries soft warnings (e.g. host-glob normalization
// notices) that callers should log. Warnings are never set when the error is
// non-nil.
func Load(path string) (Config, []string, error) {
	src, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		defaults := DefaultConfig()
		if err := Save(defaults, path); err != nil {
			return Config{}, nil, err
		}
		return defaults, nil, nil
	}
	if err != nil {
		return Config{}, nil, fmt.Errorf("config: read %s: %w", path, err)
	}

	w, err := parseHCL(src, path)
	if err != nil {
		return Config{}, nil, fmt.Errorf("config: parse %s: %w", path, err)
	}

	// Merge: start from defaults and overwrite with parsed wire values.
	// Because HCL only populates attributes present in the file, zero values
	// in the wire struct indicate "not set". We apply non-zero overrides.
	defaults := DefaultConfig()
	if err := mergeWire(&defaults, w); err != nil {
		return Config{}, nil, fmt.Errorf("config: %w", err)
	}

	warnings, err := validateConfig(&defaults)
	if err != nil {
		return Config{}, nil, fmt.Errorf("config: %s: %w", path, err)
	}

	return defaults, warnings, nil
}

// ---------------------------------------------------------------------------
// Refresh
// ---------------------------------------------------------------------------

// Refresh loads the config at path and saves it back, backfilling any new
// defaults introduced by upgrades. Any load-time warnings are discarded; the
// caller is expected to surface them via a prior Load call if interactive.
func Refresh(path string) error {
	cfg, _, err := Load(path)
	if err != nil {
		return fmt.Errorf("config: refresh: %w", err)
	}
	return Save(cfg, path)
}

// ---------------------------------------------------------------------------
// merge helpers
// ---------------------------------------------------------------------------

// mergeWire applies non-zero wire values onto the typed defaults in place.
// Duration and size fields are converted; only non-zero strings/ints override.
// Returns an error if any duration or size value fails to parse.
func mergeWire(dst *Config, w wireConfig) error {
	if w.Proxy.Listen != "" {
		dst.Proxy.Listen = w.Proxy.Listen
	}
	if w.Dashboard.Listen != "" {
		dst.Dashboard.Listen = w.Dashboard.Listen
	}
	// open_browser: *bool — nil means "not present in file"; only overwrite when set.
	if w.Dashboard.OpenBrowser != nil {
		dst.Dashboard.OpenBrowser = *w.Dashboard.OpenBrowser
	}
	if w.Rules.Dir != "" {
		dst.Rules.Dir = w.Rules.Dir
	}
	if w.Secrets.CacheTTL != "" {
		d, err := parseDur(w.Secrets.CacheTTL)
		if err != nil {
			return fmt.Errorf("secrets.cache_ttl: %w", err)
		}
		dst.Secrets.CacheTTL = d
	}
	if w.Audit.RetentionDays != 0 {
		dst.Audit.RetentionDays = w.Audit.RetentionDays
	}
	if w.Audit.PruneAt != "" {
		dst.Audit.PruneAt = w.Audit.PruneAt
	}
	if w.Approval.Timeout != "" {
		d, err := parseDur(w.Approval.Timeout)
		if err != nil {
			return fmt.Errorf("approval.timeout: %w", err)
		}
		dst.Approval.Timeout = d
	}
	if w.Approval.MaxPending != 0 {
		dst.Approval.MaxPending = w.Approval.MaxPending
	}
	if len(w.ProxyBehavior.NoInterceptHosts) > 0 {
		dst.ProxyBehavior.NoInterceptHosts = w.ProxyBehavior.NoInterceptHosts
	}
	if w.ProxyBehavior.MaxBodyBuffer != "" {
		n, err := parseSize(w.ProxyBehavior.MaxBodyBuffer)
		if err != nil {
			return fmt.Errorf("proxy_behavior.max_body_buffer: %w", err)
		}
		dst.ProxyBehavior.MaxBodyBuffer = n
	}
	if err := mergeTimeouts(&dst.Timeouts, w.Timeouts); err != nil {
		return err
	}
	if w.Log.Level != "" {
		dst.Log.Level = w.Log.Level
	}
	if w.Log.Format != "" {
		dst.Log.Format = w.Log.Format
	}
	return nil
}

func mergeTimeouts(dst *TimeoutsConfig, w wireTimeouts) error {
	applyDur := func(field *time.Duration, s, name string) error {
		if s == "" {
			return nil
		}
		d, err := parseDur(s)
		if err != nil {
			return fmt.Errorf("timeouts.%s: %w", name, err)
		}
		*field = d
		return nil
	}
	if err := applyDur(&dst.ConnectReadHeader, w.ConnectReadHeader, "connect_read_header"); err != nil {
		return err
	}
	if err := applyDur(&dst.MITMHandshake, w.MITMHandshake, "mitm_handshake"); err != nil {
		return err
	}
	if err := applyDur(&dst.IdleKeepalive, w.IdleKeepalive, "idle_keepalive"); err != nil {
		return err
	}
	if err := applyDur(&dst.UpstreamDial, w.UpstreamDial, "upstream_dial"); err != nil {
		return err
	}
	if err := applyDur(&dst.UpstreamTLS, w.UpstreamTLS, "upstream_tls"); err != nil {
		return err
	}
	if err := applyDur(&dst.UpstreamResponseHeader, w.UpstreamResponseHeader, "upstream_response_header"); err != nil {
		return err
	}
	if err := applyDur(&dst.UpstreamIdleKeepalive, w.UpstreamIdleKeepalive, "upstream_idle_keepalive"); err != nil {
		return err
	}
	if err := applyDur(&dst.BodyBufferRead, w.BodyBufferRead, "body_buffer_read"); err != nil {
		return err
	}
	if err := applyDur(&dst.RequestBodyRead, w.RequestBodyRead, "request_body_read"); err != nil {
		return err
	}
	if err := applyDur(&dst.ResponseBodyRead, w.ResponseBodyRead, "response_body_read"); err != nil {
		return err
	}
	return nil
}

// ---------------------------------------------------------------------------
// HCL renderer
// ---------------------------------------------------------------------------

// renderHCL serialises a Config back to an HCL string.
func renderHCL(cfg Config) string {
	fmtDur := func(d time.Duration) string {
		if d == 0 {
			return "0s"
		}
		return d.String()
	}
	fmtSize := func(n int64) string {
		switch {
		case n%(1<<30) == 0:
			return fmt.Sprintf("%dGiB", n/(1<<30))
		case n%(1<<20) == 0:
			return fmt.Sprintf("%dMiB", n/(1<<20))
		case n%(1<<10) == 0:
			return fmt.Sprintf("%dKiB", n/(1<<10))
		default:
			return strconv.FormatInt(n, 10)
		}
	}
	fmtBool := func(b bool) string {
		if b {
			return "true"
		}
		return "false"
	}
	fmtHosts := func(hosts []string) string {
		if len(hosts) == 0 {
			return "[]"
		}
		quoted := make([]string, len(hosts))
		for i, h := range hosts {
			quoted[i] = fmt.Sprintf("%q", h)
		}
		return "[" + strings.Join(quoted, ", ") + "]"
	}

	return fmt.Sprintf(`proxy {
  listen = %q
}

dashboard {
  listen       = %q
  open_browser = %s
}

rules {
  dir = %q
}

secrets {
  cache_ttl = %q
}

audit {
  retention_days = %d
  prune_at       = %q
}

approval {
  timeout     = %q
  max_pending = %d
}

proxy_behavior {
  no_intercept_hosts = %s
  max_body_buffer    = %q
}

timeouts {
  connect_read_header      = %q
  mitm_handshake           = %q
  idle_keepalive           = %q
  upstream_dial            = %q
  upstream_tls             = %q
  upstream_response_header = %q
  upstream_idle_keepalive  = %q
  body_buffer_read         = %q
  request_body_read        = %q
  response_body_read       = %q
}

log {
  level  = %q
  format = %q
}
`,
		cfg.Proxy.Listen,
		cfg.Dashboard.Listen,
		fmtBool(cfg.Dashboard.OpenBrowser),
		cfg.Rules.Dir,
		fmtDur(cfg.Secrets.CacheTTL),
		cfg.Audit.RetentionDays,
		cfg.Audit.PruneAt,
		fmtDur(cfg.Approval.Timeout),
		cfg.Approval.MaxPending,
		fmtHosts(cfg.ProxyBehavior.NoInterceptHosts),
		fmtSize(cfg.ProxyBehavior.MaxBodyBuffer),
		fmtDur(cfg.Timeouts.ConnectReadHeader),
		fmtDur(cfg.Timeouts.MITMHandshake),
		fmtDur(cfg.Timeouts.IdleKeepalive),
		fmtDur(cfg.Timeouts.UpstreamDial),
		fmtDur(cfg.Timeouts.UpstreamTLS),
		fmtDur(cfg.Timeouts.UpstreamResponseHeader),
		fmtDur(cfg.Timeouts.UpstreamIdleKeepalive),
		fmtDur(cfg.Timeouts.BodyBufferRead),
		fmtDur(cfg.Timeouts.RequestBodyRead),
		fmtDur(cfg.Timeouts.ResponseBodyRead),
		cfg.Log.Level,
		cfg.Log.Format,
	)
}
