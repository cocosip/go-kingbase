package kingbase

import (
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

const (
	testUser            = "alice"
	testDatabase        = "tenant"
	testHostName        = "localhost"
	testWindows         = "windows"
	testSearchPath      = "public"
	testSeverity        = "ERROR"
	testSeverityFatal   = "FATAL"
	testSQLStateAuth    = "28P01"
	testAuthFailed      = "password authentication failed"
	testHello           = "hello"
	testAbandoned       = "abandoned"
	testDuplicateKey    = "duplicate key value"
	testBob             = "bob"
	testColumnName      = "name"
	testSQLStateNoTable = "42P01"
	testTypeBool        = "bool"
	testJSONBody        = "{\"a\":1}"
)

func mustParse(t *testing.T, dsn string) *Config {
	t.Helper()
	cfg, err := ParseConfig(dsn)
	if err != nil {
		t.Fatalf("ParseConfig(%q) returned error: %v", dsn, err)
	}
	return cfg
}

func TestParseConfigURL(t *testing.T) {
	dsn := "kingbase://app:se%40cret@db.example:5555/tenant" +
		"?compatibilityMode=MYSQL&wireProtocol=pg&binary_parameters=yes" +
		"&disable_prepared_binary_result=yes&get_last_insert_id=yes" +
		"&sslMode=verify-full&sslcert=/certs/client.crt&sslkey=/certs/client.key" +
		"&sslrootcert=/certs/ca.crt&connect_timeout=15" +
		"&application_name=svc&fallback_application_name=fb" +
		"&client_encoding=utf8&options=-c%20search_path%3Dx&search_path=public"
	cfg := mustParse(t, dsn)

	if cfg.Network != networkTCP {
		t.Errorf("Network = %q, want tcp", cfg.Network)
	}
	if cfg.Host != "db.example" {
		t.Errorf("Host = %q, want db.example", cfg.Host)
	}
	if cfg.Port != 5555 {
		t.Errorf("Port = %d, want 5555", cfg.Port)
	}
	if cfg.User != "app" {
		t.Errorf("User = %q, want app", cfg.User)
	}
	if cfg.Password != "se@cret" {
		t.Errorf("Password = %q, want decoded se@cret", cfg.Password)
	}
	if cfg.Database != testDatabase {
		t.Errorf("Database = %q, want tenant", cfg.Database)
	}
	if cfg.CompatibilityMode != ModeMySQL {
		t.Errorf("CompatibilityMode = %v, want mysql", cfg.CompatibilityMode)
	}
	if cfg.WireProtocol != WirePG {
		t.Errorf("WireProtocol = %v, want pg", cfg.WireProtocol)
	}
	if !cfg.BinaryParameters || !cfg.DisablePreparedBinaryResult || !cfg.GetLastInsertID {
		t.Errorf("boolean parameters = %v/%v/%v, want all true",
			cfg.BinaryParameters, cfg.DisablePreparedBinaryResult, cfg.GetLastInsertID)
	}
	if cfg.SSLMode != SSLVerifyFull {
		t.Errorf("SSLMode = %v, want verify-full", cfg.SSLMode)
	}
	if cfg.SSLCert != "/certs/client.crt" || cfg.SSLKey != "/certs/client.key" || cfg.SSLRootCert != "/certs/ca.crt" {
		t.Errorf("TLS material = %q/%q/%q, want configured paths",
			cfg.SSLCert, cfg.SSLKey, cfg.SSLRootCert)
	}
	if cfg.ConnectTimeout != 15*time.Second {
		t.Errorf("ConnectTimeout = %v, want 15s", cfg.ConnectTimeout)
	}
	if cfg.ApplicationName != "svc" || cfg.FallbackApplicationName != "fb" {
		t.Errorf("application names = %q/%q, want svc/fb", cfg.ApplicationName, cfg.FallbackApplicationName)
	}
	if cfg.ClientEncoding != encodingUTF8 {
		t.Errorf("ClientEncoding = %q, want UTF8", cfg.ClientEncoding)
	}
	if cfg.Options != "-c search_path=x" {
		t.Errorf("Options = %q, want -c search_path=x", cfg.Options)
	}
	if cfg.SearchPath != testSearchPath {
		t.Errorf("SearchPath = %q, want public", cfg.SearchPath)
	}
}

func TestParseConfigEquivalentURLs(t *testing.T) {
	want := mustParse(t, "kingbase://u:p@h:54321/db?compatibilityMode=mysql&sslmode=verify-full")
	equivalent := []string{
		"kingbase://u:p@h:54321/db?compatibilityMode=MYSQL&SSLMode=VERIFY-FULL",
		"kingbase://u:p@h:54321/db?COMPATIBILITYMODE=mysql&sslmode=verify-full",
		"kingbase://u:p@h:54321/db?compatibleMode=MySql&sslmode=verify-full",
		"kingbase://u:p@h:54321/db?mode=mysql&sslmode=verify-full",
		"postgres://u:p@h:54321/db?mode=mysql&sslmode=verify-full",
		"postgresql://u:p@h:54321/db?mode=mysql&sslmode=verify-full",
	}
	for _, dsn := range equivalent {
		got := mustParse(t, dsn)
		if !reflect.DeepEqual(*got, *want) {
			t.Errorf("ParseConfig(%q) = %+v, want %+v", dsn, *got, *want)
		}
	}
}

func TestParseConfigKeyValue(t *testing.T) {
	dsn := `host=db.example port=6666 user='a b' password='' dbname='tenant db' ` +
		`options='-c search_path="a b"' application_name=svc sslmode=disable connect_timeout=0`
	cfg := mustParse(t, dsn)

	if cfg.Host != "db.example" || cfg.Port != 6666 {
		t.Errorf("endpoint = %q:%d, want db.example:6666", cfg.Host, cfg.Port)
	}
	if cfg.User != "a b" {
		t.Errorf("User = %q, want %q", cfg.User, "a b")
	}
	if cfg.Password != "" {
		t.Errorf("Password = %q, want explicit empty", cfg.Password)
	}
	if cfg.Database != "tenant db" {
		t.Errorf("Database = %q, want %q", cfg.Database, "tenant db")
	}
	if cfg.Options != `-c search_path="a b"` {
		t.Errorf("Options = %q, want preserved quoted value", cfg.Options)
	}
	if cfg.SSLMode != SSLDisable {
		t.Errorf("SSLMode = %v, want disable", cfg.SSLMode)
	}
	if cfg.ConnectTimeout != 0 {
		t.Errorf("ConnectTimeout = %v, want 0", cfg.ConnectTimeout)
	}
}

func TestParseConfigKeyValueEscapes(t *testing.T) {
	cfg := mustParse(t, `user=a\ b password=pa\\ss dbname=x`)
	if cfg.User != "a b" {
		t.Errorf("User = %q, want %q", cfg.User, "a b")
	}
	if cfg.Password != `pa\ss` {
		t.Errorf("Password = %q, want backslash-preserved value", cfg.Password)
	}
	if cfg.Database != "x" {
		t.Errorf("Database = %q, want x", cfg.Database)
	}
}

func TestParseConfigAliasesAndDuplicates(t *testing.T) {
	cfg := mustParse(t, "kingbase://u:p@h/db?connect_timeout=5&connectTimeout=7&mode=oracle&compatibilityMode=mysql")
	if cfg.ConnectTimeout != 7*time.Second {
		t.Errorf("ConnectTimeout = %v, want last occurrence 7s", cfg.ConnectTimeout)
	}
	if cfg.CompatibilityMode != ModeMySQL {
		t.Errorf("CompatibilityMode = %v, want last occurrence mysql", cfg.CompatibilityMode)
	}

	cfg = mustParse(t, "kingbase://u:p@h/db?sslmode=disable&SSL_MODE=require")
	if cfg.SSLMode != SSLRequire {
		t.Errorf("SSLMode = %v, want last occurrence require", cfg.SSLMode)
	}

	cfg = mustParse(t, "kingbase://u:p@h/db?user=q&password=w")
	if cfg.User != "q" || cfg.Password != "w" {
		t.Errorf("credentials = %q/%q, want query values q/w", cfg.User, cfg.Password)
	}
}

func TestParseConfigValuesKeepCase(t *testing.T) {
	cfg := mustParse(t, "kingbase://AppUser:PassWord@h/MyDB?application_name=MyService&search_path=MySchema")
	if cfg.User != "AppUser" {
		t.Errorf("User = %q, want case preserved", cfg.User)
	}
	if cfg.Password != "PassWord" {
		t.Errorf("Password = %q, want case preserved", cfg.Password)
	}
	if cfg.Database != "MyDB" {
		t.Errorf("Database = %q, want case preserved", cfg.Database)
	}
	if cfg.ApplicationName != "MyService" || cfg.SearchPath != "MySchema" {
		t.Errorf("runtime values = %q/%q, want case preserved", cfg.ApplicationName, cfg.SearchPath)
	}
}

func TestParseConfigPercentDecoding(t *testing.T) {
	cfg := mustParse(t, "kingbase://app%40tenant:p@h/db?compatibility%4Dode=mysql")
	if cfg.User != "app@tenant" {
		t.Errorf("User = %q, want percent-decoded app@tenant", cfg.User)
	}
	if cfg.CompatibilityMode != ModeMySQL {
		t.Errorf("CompatibilityMode = %v, want mysql from decoded key", cfg.CompatibilityMode)
	}

	if _, err := ParseConfig("kingbase://u@h/db?sslmode=%zz"); err == nil {
		t.Error("ParseConfig accepted a malformed percent escape")
	}
}

func TestParseConfigDefaults(t *testing.T) {
	cfg := mustParse(t, "")
	if cfg.Network != networkTCP || cfg.Host != testHostName || cfg.Port != defaultPort {
		t.Errorf("endpoint = %q %q:%d, want tcp localhost:%d", cfg.Network, cfg.Host, cfg.Port, defaultPort)
	}
	if cfg.CompatibilityMode != ModeAuto || cfg.WireProtocol != WirePG {
		t.Errorf("mode/transport = %v/%v, want auto/pg", cfg.CompatibilityMode, cfg.WireProtocol)
	}
	if cfg.SSLMode != SSLRequire {
		t.Errorf("SSLMode = %v, want require", cfg.SSLMode)
	}
	if cfg.ClientEncoding != encodingUTF8 {
		t.Errorf("ClientEncoding = %q, want UTF8", cfg.ClientEncoding)
	}
	if cfg.ConnectTimeout != 0 {
		t.Errorf("ConnectTimeout = %v, want 0 (unlimited)", cfg.ConnectTimeout)
	}
	if cfg.BinaryParameters || cfg.DisablePreparedBinaryResult || cfg.GetLastInsertID {
		t.Error("boolean parameters defaulted to true")
	}
}

func TestParseConfigMissingDatabaseDefaultsToUser(t *testing.T) {
	cfg := mustParse(t, "kingbase://alice@h:54321")
	if cfg.Database != testUser {
		t.Errorf("Database = %q, want user name alice", cfg.Database)
	}

	cfg = mustParse(t, "user=bob")
	if cfg.Database != testBob {
		t.Errorf("Database = %q, want user name bob", cfg.Database)
	}
}

func TestParseConfigEmptyPasswordPreserved(t *testing.T) {
	cfg := mustParse(t, "kingbase://u:@h/db")
	if cfg.Password != "" {
		t.Errorf("Password = %q, want explicit empty", cfg.Password)
	}
	cfg = mustParse(t, "user=u password= dbname=d")
	if cfg.Password != "" {
		t.Errorf("Password = %q, want explicit empty", cfg.Password)
	}
}

func TestParseConfigUnixSocket(t *testing.T) {
	_, err := ParseConfig("host=/var/run/kingbase dbname=d")
	if runtime.GOOS == testWindows {
		if err == nil || !strings.Contains(err.Error(), "unix") {
			t.Fatalf("ParseConfig(unix socket) on windows error = %v, want unix unsupported", err)
		}
		return
	}
	if err != nil {
		t.Fatalf("ParseConfig(unix socket) error: %v", err)
	}
	cfg := mustParse(t, "host=/var/run/kingbase dbname=d")
	if cfg.Network != "unix" || cfg.SSLMode != SSLDisable {
		t.Errorf("network/sslmode = %q/%v, want unix/disable", cfg.Network, cfg.SSLMode)
	}
	if _, err := ParseConfig("host=/var/run/kingbase sslmode=require"); err == nil {
		t.Error("ParseConfig accepted TLS on a unix socket")
	}
	if _, err := ParseConfig("host=/var/run/kingbase sslrootcert=/ca.pem"); err == nil {
		t.Error("ParseConfig accepted TLS material on a unix socket")
	}
}

const (
	errUnknownParam = "unknown configuration parameter"
	errInvalidPort  = "invalid port"
	errNotSupported = "not supported yet"
)

func TestParseConfigErrors(t *testing.T) {
	cases := []struct {
		name string
		dsn  string
		want string
	}{
		{"unknown key", "host=h bogus=1", errUnknownParam},
		{"unknown url key", "kingbase://h/db?bogus=1", errUnknownParam},
		{"bad sslmode", "sslmode=bogus", "unknown sslmode"},
		{"bad timeout", "connect_timeout=-1", "invalid connect_timeout"},
		{"non numeric timeout", "connect_timeout=soon", "invalid connect_timeout"},
		{"bad port", "port=0", errInvalidPort},
		{"port too large", "port=70000", errInvalidPort},
		{"non numeric port", "port=x", errInvalidPort},
		{"bad bool", "binary_parameters=maybe", "invalid binary_parameters"},
		{"mysql transport", "wireProtocol=mysql", errNotSupported},
		{"bad transport", "wireProtocol=bogus", "unknown wireProtocol"},
		{"bad encoding", "client_encoding=latin1", "unsupported client_encoding"},
		{"target session attrs primary", "target_session_attrs=primary", "not supported yet"},
		{"bad target session attrs", "target_session_attrs=maybe", "unknown target_session_attrs"},
		{"empty host entry", "host=a,,b", "invalid host"},
		{"host with spaces", "host='a b'", "invalid host"},
		{"unix host in list", "host=/tmp/kb,b", "unix socket host cannot be combined"},
		{"port list longer than hosts", "host=a port=1111,2222", "cannot be matched"},
		{"bad port list", "port=1,x", errInvalidPort},
		{"bad scheme", "mysql://h/db", "unsupported DSN scheme"},
		{"missing equals", "host", "invalid key=value DSN"},
		{"unterminated quote", "host='abc", "unterminated quoted DSN value"},
		{"trailing backslash", `host=abc\`, "trailing backslash"},
		{"invalid dev null", "h\x00st=x", errUnknownParam},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseConfig(tc.dsn)
			if err == nil {
				t.Fatalf("ParseConfig(%q) succeeded, want error containing %q", tc.dsn, tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want containing %q", err, tc.want)
			}
		})
	}
}

func TestParseConfigQueryOverridesEndpoint(t *testing.T) {
	cfg := mustParse(t, "kingbase://u:p@h1:1111/db1?host=h2&port=2222&dbname=db2")
	if cfg.Host != "h2" || cfg.Port != 2222 || cfg.Database != "db2" {
		t.Errorf("endpoint = %s:%d/%s, want h2:2222/db2", cfg.Host, cfg.Port, cfg.Database)
	}
}

func TestParseConfigDuplicatesUseLastValidValue(t *testing.T) {
	cfg := mustParse(t, "kingbase://u@h/db?connect_timeout=bad&connect_timeout=5")
	if cfg.ConnectTimeout != 5*time.Second {
		t.Errorf("ConnectTimeout = %v, want 5s from last valid occurrence", cfg.ConnectTimeout)
	}
	cfg = mustParse(t, "kingbase://u@h/db?port=0&port=5432")
	if cfg.Port != 5432 {
		t.Errorf("Port = %d, want 5432 from last valid occurrence", cfg.Port)
	}
	cfg = mustParse(t, "kingbase://u@h/db?mode=bogus&compatibilityMode=mysql")
	if cfg.CompatibilityMode != ModeMySQL {
		t.Errorf("CompatibilityMode = %v, want mysql from last valid occurrence", cfg.CompatibilityMode)
	}
}

func TestParseConfigKeyValueWithURLValue(t *testing.T) {
	cfg := mustParse(t, "user=u password='pa://ss' host=h")
	if cfg.Password != "pa://ss" {
		t.Errorf("Password = %q, want libpq value containing ://", cfg.Password)
	}
}

func TestParseConfigAliasDatabaseMode(t *testing.T) {
	cfg := mustParse(t, "databaseMode=SQLServer")
	if cfg.CompatibilityMode != ModeSQLServer {
		t.Errorf("CompatibilityMode = %v, want sqlserver from databaseMode alias", cfg.CompatibilityMode)
	}
}

func TestParseConfigDecodedEnumValue(t *testing.T) {
	cfg := mustParse(t, "kingbase://u@h/db?compatibilityMode=%6Dysql")
	if cfg.CompatibilityMode != ModeMySQL {
		t.Errorf("CompatibilityMode = %v, want percent-decoded mysql", cfg.CompatibilityMode)
	}
}

func TestParseConfigExplicitEmptyHostAndDatabase(t *testing.T) {
	cfg := mustParse(t, "user=u password= dbname= host=")
	if cfg.Database != "u" {
		t.Errorf("Database = %q, want user fallback for empty database", cfg.Database)
	}
	if cfg.Host != testHostName {
		t.Errorf("Host = %q, want localhost for empty host", cfg.Host)
	}
	if cfg.Password != "" {
		t.Errorf("Password = %q, want explicit empty", cfg.Password)
	}
}

func TestParseConfigErrorsDoNotLeakPassword(t *testing.T) {
	dsns := []string{
		"kingbase://alice:s3cret@h:bad/db",
		"password 's3cret'",
	}
	for _, dsn := range dsns {
		_, err := ParseConfig(dsn)
		if err == nil {
			t.Fatalf("ParseConfig(%q) succeeded, want error", dsn)
		}
		if strings.Contains(err.Error(), "s3cret") {
			t.Errorf("error for %q leaks the password: %v", dsn, err)
		}
	}
}

func TestParseConfigUnixSocketWithoutTLS(t *testing.T) {
	if runtime.GOOS == testWindows {
		t.Skip("unix sockets are rejected on windows")
	}
	cfg := mustParse(t, "host=/tmp/kb sslmode=disable user=u dbname=d")
	if cfg.Network != "unix" || cfg.SSLMode != SSLDisable {
		t.Errorf("network/sslmode = %q/%v, want unix/disable", cfg.Network, cfg.SSLMode)
	}
}

func TestParseConfigRejectsNULBytes(t *testing.T) {
	dsns := []string{
		"kingbase://u:pa%00ss@h/db",
		"user=u application_name=bad\x00value",
	}
	for _, dsn := range dsns {
		_, err := ParseConfig(dsn)
		if err == nil || !strings.Contains(err.Error(), "NUL") {
			t.Errorf("ParseConfig(%q) error = %v, want NUL rejection", dsn, err)
		}
	}
}

func TestParseConfigTimeoutOverflow(t *testing.T) {
	_, err := ParseConfig("connect_timeout=9223372037")
	if err == nil || !strings.Contains(err.Error(), "invalid connect_timeout") {
		t.Fatalf("error = %v, want invalid connect_timeout", err)
	}
}

func TestParseConfigBoolValues(t *testing.T) {
	for _, value := range []string{"yes", "true", "on", "1", "YES", "True"} {
		cfg := mustParse(t, "binary_parameters="+value)
		if !cfg.BinaryParameters {
			t.Errorf("binary_parameters=%s parsed as false", value)
		}
	}
	for _, value := range []string{"no", "false", "off", "0", "NO", "False"} {
		cfg := mustParse(t, "get_last_insert_id="+value)
		if cfg.GetLastInsertID {
			t.Errorf("get_last_insert_id=%s parsed as true", value)
		}
	}
	cfg := mustParse(t, "get_auto_increment_id=yes")
	if !cfg.GetLastInsertID {
		t.Error("get_auto_increment_id alias was not accepted")
	}
}

func TestConfigKeepAlives(t *testing.T) {
	cases := []struct {
		name string
		dsn  string
		want bool
		idle time.Duration
	}{
		{name: "default enabled", dsn: "", want: true},
		{name: "disabled", dsn: "keepalives=0", want: false},
		{name: "explicit enabled", dsn: "keepalives=1", want: true},
		{name: "idle seconds", dsn: "keepalives=1 keepalives_idle=30", want: true, idle: 30 * time.Second},
		{name: "idle without toggle", dsn: "keepalives_idle=15", want: true, idle: 15 * time.Second},
		{name: "idle zero keeps default", dsn: "keepalives_idle=0", want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := ParseConfig("user=alice dbname=tenant " + tc.dsn)
			if err != nil {
				t.Fatalf("ParseConfig: %v", err)
			}
			if cfg.KeepAlives != tc.want {
				t.Errorf("KeepAlives = %v, want %v", cfg.KeepAlives, tc.want)
			}
			if cfg.KeepAlivesIdle != tc.idle {
				t.Errorf("KeepAlivesIdle = %v, want %v", cfg.KeepAlivesIdle, tc.idle)
			}
		})
	}

	invalid := []string{
		"keepalives=maybe",
		"keepalives_idle=-1",
		"keepalives_idle=abc",
		"keepalives_interval=5",
		"keepalives_count=3",
	}
	for _, dsn := range invalid {
		if _, err := ParseConfig("user=alice dbname=tenant " + dsn); err == nil {
			t.Errorf("ParseConfig(%q) succeeded, want error", dsn)
		}
	}
}
