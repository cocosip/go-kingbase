package kingbase

import (
	"context"
	"database/sql"
	"database/sql/driver"
)

func init() {
	sql.Register("kingbase", &Driver{})
}

// Driver is the database/sql driver for KingbaseES.
type Driver struct{}

// Open opens a physical connection from a DSN.
func (d *Driver) Open(name string) (driver.Conn, error) {
	connector, err := d.OpenConnector(name)
	if err != nil {
		return nil, err
	}
	return connector.Connect(context.Background())
}

// OpenConnector parses the DSN and returns a reusable driver.Connector.
func (d *Driver) OpenConnector(name string) (driver.Connector, error) {
	cfg, err := ParseConfig(name)
	if err != nil {
		return nil, err
	}
	return &Connector{cfg: cfg}, nil
}
