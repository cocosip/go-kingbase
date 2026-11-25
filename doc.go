// Package kingbase implements a database/sql driver for KingbaseES.
//
// The driver registers itself as "kingbase". Initial development targets the
// V9 PG-wire profiles; the compatibility mode is declared in the connection
// string or discovered from the server, and the type catalog is read from the
// server at connect time. There is no user-selected product version and no
// built-in OID table.
//
// The architectural contract, feature matrix and delivery plan live in docs/:
// go-driver-architecture-design.md, go-driver-capabilities.md and
// go-driver-development.md.
package kingbase
