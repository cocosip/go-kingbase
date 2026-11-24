package gokb

import (
	"errors"
	"fmt"
	"math"
	"net"
	"net/url"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// defaultPort is the conventional KingbaseES TCP port. It is a default for
// configuration only, never a detection or fallback rule.
const defaultPort = 54321

// CompatibilityMode declares the KingbaseES compatibility semantics the client
// must apply. It never changes the server mode; the server report is
// authoritative and a conflict fails at connect.
type CompatibilityMode int

const (
	modeMySQLName = "mysql"
	unknownName   = "unknown"

	networkTCP      = "tcp"
	networkUnix     = "unix"
	encodingUTF8    = "UTF8"
	startupUser     = "user"
	startupDatabase = "database"
	startupEncoding = "client_encoding"
)

// Supported compatibility modes. ModeAuto requires authoritative server
// evidence at connect; it is not a default-semantics guess.
const (
	ModeAuto CompatibilityMode = iota
	ModePG
	ModeOracle
	ModeMySQL
	ModeSQLServer
)

// String returns the normalized mode name.
func (m CompatibilityMode) String() string {
	switch m {
	case ModeAuto:
		return "auto"
	case ModePG:
		return "pg"
	case ModeOracle:
		return "oracle"
	case ModeMySQL:
		return modeMySQLName
	case ModeSQLServer:
		return "sqlserver"
	default:
		return unknownName
	}
}

func parseCompatibilityMode(value string) (CompatibilityMode, error) {
	switch strings.ToLower(value) {
	case "auto":
		return ModeAuto, nil
	case "pg":
		return ModePG, nil
	case "oracle":
		return ModeOracle, nil
	case modeMySQLName:
		return ModeMySQL, nil
	case "sqlserver":
		return ModeSQLServer, nil
	default:
		return ModeAuto, fmt.Errorf("kingbase: unknown compatibilityMode %q (want auto, pg, oracle, mysql or sqlserver)", value)
	}
}

// WireProtocol selects the transport. It is independent of the compatibility
// mode: a mode never selects a handshake.
type WireProtocol int

// Supported transports. MySQL is a conditional backend and is rejected until
// it is implemented.
const (
	WirePG WireProtocol = iota
	WireMySQL
)

// String returns the normalized transport name.
func (w WireProtocol) String() string {
	if w == WireMySQL {
		return modeMySQLName
	}
	return "pg"
}

// SSLMode is the requested TLS trust policy for TCP connections.
type SSLMode int

// Supported TLS trust policies.
const (
	SSLDisable SSLMode = iota
	SSLRequire
	SSLVerifyCA
	SSLVerifyFull
)

// String returns the normalized sslmode name.
func (m SSLMode) String() string {
	switch m {
	case SSLDisable:
		return "disable"
	case SSLRequire:
		return "require"
	case SSLVerifyCA:
		return "verify-ca"
	case SSLVerifyFull:
		return "verify-full"
	default:
		return unknownName
	}
}

func parseSSLMode(value string) (SSLMode, error) {
	switch strings.ToLower(value) {
	case "disable":
		return SSLDisable, nil
	case "require":
		return SSLRequire, nil
	case "verify-ca":
		return SSLVerifyCA, nil
	case "verify-full":
		return SSLVerifyFull, nil
	default:
		return SSLDisable, fmt.Errorf("kingbase: unknown sslmode %q (want disable, require, verify-ca or verify-full)", value)
	}
}

// Config is a parsed connection configuration. ParseConfig returns an
// immutable value: no later DSN, environment or server input mutates it.
type Config struct {
	// Network is "tcp" or "unix".
	Network string
	// Host and Port are the first configured endpoint.
	Host string
	Port int
	// Hosts and Ports hold the expanded opt-in endpoint list; both are empty
	// for a single endpoint. A shorter list repeats its last entry.
	Hosts []string
	Ports []int

	User string
	// Password is kept verbatim and must never appear in diagnostics.
	Password string
	Database string

	CompatibilityMode CompatibilityMode
	WireProtocol      WireProtocol

	SSLMode     SSLMode
	SSLCert     string
	SSLKey      string
	SSLRootCert string

	ConnectTimeout          time.Duration
	KeepAlives              bool
	KeepAlivesIdle          time.Duration
	ApplicationName         string
	FallbackApplicationName string
	ClientEncoding          string

	// TargetSessionAttrs selects the session kind a host must offer. Any means
	// no target check is performed.
	TargetSessionAttrs TargetSessionAttrs

	// NoticeHandler receives server notices; nil discards them.
	NoticeHandler NoticeHandler

	BinaryParameters            bool
	DisablePreparedBinaryResult bool
	GetLastInsertID             bool

	Options    string
	SearchPath string
}

// TargetSessionAttrs is the opt-in target-selection policy for a host list.
type TargetSessionAttrs int

// Supported target-selection policies.
const (
	TargetSessionAny TargetSessionAttrs = iota
	TargetSessionReadWrite
	TargetSessionReadOnly
)

// String returns the normalized policy name.
func (t TargetSessionAttrs) String() string {
	switch t {
	case TargetSessionReadWrite:
		return "read-write"
	case TargetSessionReadOnly:
		return "read-only"
	default:
		return "any"
	}
}

func parseTargetSessionAttrs(value string) (TargetSessionAttrs, error) {
	switch normalizeParamName(value) {
	case "any":
		return TargetSessionAny, nil
	case "read-write", "readwrite":
		return TargetSessionReadWrite, nil
	case "read-only", "readonly":
		return TargetSessionReadOnly, nil
	case "primary", "standby", "prefer-standby", "preferstandby":
		return TargetSessionAny, fmt.Errorf("kingbase: target_session_attrs %q is not supported yet (it needs live cluster evidence)", value)
	default:
		return TargetSessionAny, fmt.Errorf("kingbase: unknown target_session_attrs %q (want any, read-write or read-only)", value)
	}
}

// ParseConfig parses a DSN in URL or libpq key=value form. It applies the
// documented normalization, aliases, defaults and validation, and fails on
// unknown or explicitly unsupported parameters. It never contacts a server.
func ParseConfig(dsn string) (*Config, error) {
	cfg := &Config{
		Network:           networkTCP,
		Host:              "localhost",
		Port:              defaultPort,
		CompatibilityMode: ModeAuto,
		WireProtocol:      WirePG,
		SSLMode:           SSLRequire,
		ClientEncoding:    encodingUTF8,
		KeepAlives:        true,
	}

	var params []parameter
	var err error
	if isURLDSN(dsn) {
		params, err = parseURLDSN(dsn)
	} else {
		params, err = parseKeyValueDSN(dsn)
	}
	if err != nil {
		return nil, err
	}

	kinds := make([]paramKind, len(params))
	for i, p := range params {
		kind := lookupParam(p.key)
		if kind == paramUnknown {
			return nil, fmt.Errorf("kingbase: unknown configuration parameter %q", p.key)
		}
		kinds[i] = kind
	}
	// Duplicate normalized names use the last input occurrence, so an earlier
	// invalid value must not reject a later valid one.
	last := make(map[paramKind]int, len(params))
	for i, kind := range kinds {
		last[kind] = i
	}
	explicit := make(map[paramKind]bool, len(last))
	for i, kind := range kinds {
		if last[kind] != i {
			continue
		}
		// Startup parameters are NUL-terminated on the wire: a NUL would
		// truncate the packet and could inject extra parameters.
		if strings.IndexByte(params[i].value, 0) >= 0 {
			return nil, fmt.Errorf("kingbase: configuration parameter %q contains a NUL byte", params[i].key)
		}
		if err := cfg.apply(kind, params[i].value); err != nil {
			return nil, err
		}
		explicit[kind] = true
	}
	if err := cfg.validate(explicit); err != nil {
		return nil, err
	}
	return cfg, nil
}

// isURLDSN reports whether dsn begins with a valid URL scheme followed by
// "://". libpq values may legitimately contain "://" (a password, options or
// an application name), so the whole input is not judged by that substring
// alone.
func isURLDSN(dsn string) bool {
	schemeEnd := strings.Index(dsn, "://")
	if schemeEnd <= 0 {
		return false
	}
	for i := 0; i < schemeEnd; i++ {
		c := dsn[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z':
		case i > 0 && (c >= '0' && c <= '9' || c == '+' || c == '-' || c == '.'):
		default:
			return false
		}
	}
	return true
}

type parameter struct {
	key   string
	value string
}

type paramKind int

const (
	paramUnknown paramKind = iota
	paramHost
	paramPort
	paramUser
	paramPassword
	paramDatabase
	paramCompatibilityMode
	paramWireProtocol
	paramBinaryParameters
	paramDisablePreparedBinaryResult
	paramGetLastInsertID
	paramSSLMode
	paramSSLCert
	paramSSLKey
	paramSSLRootCert
	paramConnectTimeout
	paramApplicationName
	paramFallbackApplicationName
	paramClientEncoding
	paramOptions
	paramSearchPath
	paramKeepalives
	paramKeepalivesIdle
	paramKeepalivesInterval
	paramKeepalivesCount
	paramTargetSessionAttrs
)

// parameterKinds maps normalized parameter names to their meaning. Names are
// lowercased with all underscores removed; explicit aliases are listed here.
var parameterKinds = map[string]paramKind{
	"host":                        paramHost,
	"port":                        paramPort,
	"user":                        paramUser,
	"password":                    paramPassword,
	"dbname":                      paramDatabase,
	"compatibilitymode":           paramCompatibilityMode,
	"compatiblemode":              paramCompatibilityMode,
	"mode":                        paramCompatibilityMode,
	"databasemode":                paramCompatibilityMode,
	"wireprotocol":                paramWireProtocol,
	"binaryparameters":            paramBinaryParameters,
	"disablepreparedbinaryresult": paramDisablePreparedBinaryResult,
	"getlastinsertid":             paramGetLastInsertID,
	"getautoincrementid":          paramGetLastInsertID,
	"sslmode":                     paramSSLMode,
	"sslcert":                     paramSSLCert,
	"sslkey":                      paramSSLKey,
	"sslrootcert":                 paramSSLRootCert,
	"connecttimeout":              paramConnectTimeout,
	"applicationname":             paramApplicationName,
	"fallbackapplicationname":     paramFallbackApplicationName,
	"clientencoding":              paramClientEncoding,
	"options":                     paramOptions,
	"searchpath":                  paramSearchPath,
	"keepalives":                  paramKeepalives,
	"keepalivesidle":              paramKeepalivesIdle,
	"keepalivesinterval":          paramKeepalivesInterval,
	"keepalivescount":             paramKeepalivesCount,
	"targetsessionattrs":          paramTargetSessionAttrs,
}

func lookupParam(key string) paramKind {
	return parameterKinds[normalizeParamName(key)]
}

// normalizeParamName lowercases ASCII letters and removes underscores, so
// connect_timeout and connectTimeout collapse to the same name.
func normalizeParamName(key string) string {
	var b strings.Builder
	b.Grow(len(key))
	for i := 0; i < len(key); i++ {
		c := key[i]
		switch {
		case c == '_':
			continue
		case c >= 'A' && c <= 'Z':
			b.WriteByte(c + ('a' - 'A'))
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

func (c *Config) apply(kind paramKind, value string) error {
	switch kind {
	case paramHost:
		c.Host = value
	case paramPort:
		ports, err := parsePortList(value)
		if err != nil {
			return err
		}
		c.Port = ports[0]
		if len(ports) > 1 {
			c.Ports = ports
		}
	case paramUser:
		c.User = value
	case paramPassword:
		c.Password = value
	case paramDatabase:
		c.Database = value
	case paramCompatibilityMode:
		mode, err := parseCompatibilityMode(value)
		if err != nil {
			return err
		}
		c.CompatibilityMode = mode
	case paramWireProtocol:
		switch strings.ToLower(value) {
		case "pg":
			c.WireProtocol = WirePG
		case modeMySQLName:
			return fmt.Errorf("kingbase: wireProtocol %q is not supported yet", value)
		default:
			return fmt.Errorf("kingbase: unknown wireProtocol %q (want pg or mysql)", value)
		}
	case paramBinaryParameters, paramDisablePreparedBinaryResult, paramGetLastInsertID:
		return c.applyBool(kind, value)
	case paramSSLMode, paramSSLCert, paramSSLKey, paramSSLRootCert:
		return c.applyTLS(kind, value)
	case paramConnectTimeout:
		seconds, err := strconv.ParseInt(value, 10, 64)
		if err != nil || seconds < 0 || seconds > maxConnectTimeoutSeconds {
			return fmt.Errorf("kingbase: invalid connect_timeout %q (want non-negative seconds)", value)
		}
		c.ConnectTimeout = time.Duration(seconds) * time.Second
	case paramApplicationName:
		c.ApplicationName = value
	case paramFallbackApplicationName:
		c.FallbackApplicationName = value
	case paramClientEncoding:
		switch strings.ToLower(value) {
		case "utf8", "utf-8":
			c.ClientEncoding = encodingUTF8
		default:
			return fmt.Errorf("kingbase: unsupported client_encoding %q (only UTF8 is implemented)", value)
		}
	case paramOptions:
		c.Options = value
	case paramSearchPath:
		c.SearchPath = value
	case paramKeepalives:
		return c.applyBool(kind, value)
	case paramKeepalivesIdle:
		return c.applyKeepAlivesIdle(value)
	case paramKeepalivesInterval, paramKeepalivesCount:
		return fmt.Errorf("kingbase: configuration parameter %q is not supported by this driver", paramName(kind))
	case paramTargetSessionAttrs:
		attrs, err := parseTargetSessionAttrs(value)
		if err != nil {
			return err
		}
		c.TargetSessionAttrs = attrs
	default:
		return fmt.Errorf("kingbase: unknown configuration parameter %q", paramName(kind))
	}
	return nil
}

func (c *Config) applyBool(kind paramKind, value string) error {
	name := paramName(kind)
	b, err := parseBoolParameter(name, value)
	if err != nil {
		return err
	}
	switch kind {
	case paramKeepalives:
		c.KeepAlives = b
	case paramBinaryParameters:
		c.BinaryParameters = b
	case paramDisablePreparedBinaryResult:
		c.DisablePreparedBinaryResult = b
	case paramGetLastInsertID:
		c.GetLastInsertID = b
	}
	return nil
}

func (c *Config) applyTLS(kind paramKind, value string) error {
	switch kind {
	case paramSSLMode:
		mode, err := parseSSLMode(value)
		if err != nil {
			return err
		}
		c.SSLMode = mode
	case paramSSLCert:
		c.SSLCert = value
	case paramSSLKey:
		c.SSLKey = value
	case paramSSLRootCert:
		c.SSLRootCert = value
	}
	return nil
}

func paramName(kind paramKind) string {
	switch kind {
	case paramBinaryParameters:
		return "binary_parameters"
	case paramDisablePreparedBinaryResult:
		return "disable_prepared_binary_result"
	case paramGetLastInsertID:
		return "get_last_insert_id"
	case paramKeepalives:
		return "keepalives"
	case paramKeepalivesIdle:
		return "keepalives_idle"
	case paramKeepalivesInterval:
		return "keepalives_interval"
	case paramKeepalivesCount:
		return "keepalives_count"
	case paramTargetSessionAttrs:
		return "target_session_attrs"
	default:
		return unknownName
	}
}

// maxConnectTimeoutSeconds prevents a seconds-to-Duration multiplication
// overflow that would silently produce a negative (unlimited) budget.
const maxConnectTimeoutSeconds = int64(math.MaxInt64) / int64(time.Second)

func parseBoolParameter(name, value string) (bool, error) {
	switch strings.ToLower(value) {
	case "yes", "true", "on", "1":
		return true, nil
	case "no", "false", "off", "0":
		return false, nil
	default:
		return false, fmt.Errorf("kingbase: invalid %s %q (want yes or no)", name, value)
	}
}

func (c *Config) validate(explicit map[paramKind]bool) error {
	if c.Host != "" {
		hosts := strings.Split(c.Host, ",")
		for _, host := range hosts {
			if host == "" || strings.ContainsAny(host, " \t\r\n") {
				return fmt.Errorf("kingbase: invalid host %q", host)
			}
			if strings.HasPrefix(host, "/") && len(hosts) > 1 {
				return fmt.Errorf("kingbase: a unix socket host cannot be combined with other hosts")
			}
		}
		if len(hosts) > 1 {
			c.Hosts = hosts
			c.Host = hosts[0]
		}
	}
	hostCount := len(c.Hosts)
	if hostCount == 0 {
		hostCount = 1
	}
	if len(c.Ports) > hostCount {
		return fmt.Errorf("kingbase: %d ports cannot be matched to %d hosts", len(c.Ports), hostCount)
	}
	if strings.HasPrefix(c.Host, "/") {
		if runtime.GOOS == "windows" {
			return fmt.Errorf("kingbase: unix sockets are not supported on windows")
		}
		c.Network = networkUnix
		if explicit[paramSSLMode] && c.SSLMode != SSLDisable {
			return fmt.Errorf("kingbase: sslmode %q cannot be used with a unix socket", c.SSLMode)
		}
		if explicit[paramSSLCert] || explicit[paramSSLKey] || explicit[paramSSLRootCert] {
			return fmt.Errorf("kingbase: TLS material cannot be used with a unix socket")
		}
		c.SSLMode = SSLDisable
	} else {
		if c.Host == "" {
			// An explicit empty host uses the local default, matching libpq's
			// local behavior without implicitly selecting a socket.
			c.Host = "localhost"
		}
		if c.SSLMode == SSLDisable && (c.SSLCert != "" || c.SSLKey != "" || c.SSLRootCert != "") {
			// Explicit TLS material must not be silently ignored.
			return fmt.Errorf("kingbase: TLS material requires sslmode require, verify-ca or verify-full")
		}
	}
	if c.Database == "" {
		// An empty database name falls back to the user name. This is the
		// libpq convention; explicit empties are meaningful for values such as
		// password, options and application_name, not for the database.
		c.Database = c.User
	}
	return nil
}

// parseURLDSN converts a kingbase/postgres/postgresql URL into an ordered
// parameter list: authority fields first, then query fields so that query
// values override them.
func parseURLDSN(dsn string) ([]parameter, error) {
	u, err := url.Parse(dsn)
	if err != nil {
		// url.Error quotes the whole input, which would echo the password.
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			return nil, fmt.Errorf("kingbase: invalid DSN URL: %w", urlErr.Err)
		}
		return nil, fmt.Errorf("kingbase: invalid DSN URL: %w", err)
	}
	switch u.Scheme {
	case "kingbase", "postgres", "postgresql":
	default:
		return nil, fmt.Errorf("kingbase: unsupported DSN scheme %q", u.Scheme)
	}

	var params []parameter
	if u.User != nil {
		params = append(params, parameter{key: "user", value: u.User.Username()})
		if password, ok := u.User.Password(); ok {
			params = append(params, parameter{key: "password", value: password})
		}
	}
	if host := u.Hostname(); host != "" {
		params = append(params, parameter{key: "host", value: host})
	}
	if port := u.Port(); port != "" {
		params = append(params, parameter{key: "port", value: port})
	}
	if database := strings.TrimPrefix(u.Path, "/"); database != "" {
		params = append(params, parameter{key: "dbname", value: database})
	}
	query, err := parseQuery(u.RawQuery)
	if err != nil {
		return nil, err
	}
	return append(params, query...), nil
}

// parseQuery decodes URL query pairs in input order. Parsing it by hand keeps
// the global order needed by last-occurrence-wins duplicate semantics and by
// alias collisions such as compatibilityMode followed by mode.
func parseQuery(raw string) ([]parameter, error) {
	if raw == "" {
		return nil, nil
	}
	var params []parameter
	for _, pair := range strings.Split(raw, "&") {
		if pair == "" {
			continue
		}
		rawKey, rawValue, _ := strings.Cut(pair, "=")
		key, err := url.QueryUnescape(rawKey)
		if err != nil {
			return nil, fmt.Errorf("kingbase: invalid DSN query parameter name %q: %w", rawKey, err)
		}
		value, err := url.QueryUnescape(rawValue)
		if err != nil {
			return nil, fmt.Errorf("kingbase: invalid value for DSN query parameter %q: %w", key, err)
		}
		params = append(params, parameter{key: key, value: value})
	}
	return params, nil
}

// parseKeyValueDSN parses libpq form key=value pairs separated by whitespace.
// A value is quoted with single quotes or backslash-escaped to preserve
// spaces and explicit empty strings.
func parseKeyValueDSN(dsn string) ([]parameter, error) {
	var params []parameter
	rest := dsn
	for {
		rest = strings.TrimLeft(rest, " \t\n\r")
		if rest == "" {
			return params, nil
		}
		key, value, remain, err := parseKeyValuePair(rest)
		if err != nil {
			return nil, err
		}
		params = append(params, parameter{key: key, value: value})
		rest = remain
	}
}

func parseKeyValuePair(s string) (key, value, rest string, err error) {
	eq := strings.IndexByte(s, '=')
	if eq <= 0 {
		// Never quote the input here: it may contain a credential.
		return "", "", "", fmt.Errorf("kingbase: invalid key=value DSN: expected key=value")
	}
	key = s[:eq]
	if strings.ContainsAny(key, " \t\n\r") {
		return "", "", "", fmt.Errorf("kingbase: invalid DSN parameter name %q", key)
	}
	value, rest, err = parseKeyValueValue(s[eq+1:])
	if err != nil {
		return "", "", "", err
	}
	return key, value, rest, nil
}

func parseKeyValueValue(s string) (value, rest string, err error) {
	var b strings.Builder
	quoted := false
	i := 0
	if i < len(s) && s[i] == '\'' {
		quoted = true
		i++
	}
	for i < len(s) {
		c := s[i]
		switch {
		case c == '\\':
			if i+1 >= len(s) {
				return "", "", fmt.Errorf("kingbase: trailing backslash in DSN value")
			}
			b.WriteByte(s[i+1])
			i += 2
		case quoted && c == '\'':
			return b.String(), s[i+1:], nil
		case !quoted && (c == ' ' || c == '\t' || c == '\n' || c == '\r'):
			return b.String(), s[i:], nil
		default:
			b.WriteByte(c)
			i++
		}
	}
	if quoted {
		return "", "", fmt.Errorf("kingbase: unterminated quoted DSN value")
	}
	return b.String(), "", nil
}

func (c *Config) applyKeepAlivesIdle(value string) error {
	seconds, err := strconv.ParseInt(value, 10, 64)
	if err != nil || seconds < 0 || seconds > maxConnectTimeoutSeconds {
		return fmt.Errorf("kingbase: invalid keepalives_idle %q (want non-negative seconds)", value)
	}
	c.KeepAlivesIdle = time.Duration(seconds) * time.Second
	return nil
}

// parsePortList parses one port or a comma-separated port list.
func parsePortList(value string) ([]int, error) {
	fields := strings.Split(value, ",")
	ports := make([]int, 0, len(fields))
	for _, field := range fields {
		port, err := strconv.Atoi(field)
		if err != nil || port < 1 || port > 65535 {
			return nil, fmt.Errorf("kingbase: invalid port %q", value)
		}
		ports = append(ports, port)
	}
	return ports, nil
}

// endpoint is one candidate physical endpoint in configuration order.
type endpoint struct {
	network string
	address string
	host    string
}

// endpoints expands the configured host and port lists into ordered candidates.
// A shorter list repeats its last entry; validation rejects empty host entries.
func (c *Config) endpoints() []endpoint {
	hosts, ports := c.Hosts, c.Ports
	if len(hosts) == 0 {
		hosts = []string{c.Host}
	}
	if len(ports) == 0 {
		ports = []int{c.Port}
	}
	count := len(hosts)
	endpoints := make([]endpoint, count)
	for i := range endpoints {
		host := hosts[min(i, len(hosts)-1)]
		port := ports[min(i, len(ports)-1)]
		endpoints[i] = c.endpoint(host, port)
	}
	return endpoints
}

// endpoint resolves one host and port to a dial address. A host starting with
// "/" is a unix socket directory and is only accepted for a single endpoint.
func (c *Config) endpoint(host string, port int) endpoint {
	if c.Network == networkUnix {
		return endpoint{network: networkUnix, address: host + "/.s.PGSQL." + strconv.Itoa(port), host: host}
	}
	return endpoint{network: networkTCP, address: net.JoinHostPort(host, strconv.Itoa(port)), host: host}
}
