package httpcredentials

import (
	"context"
	"database/sql"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
)

// ConfigurationTx deliberately never selects material handles or availability.
// Ref is an internal join key until the export owner remaps it.
func ConfigurationTx(ctx context.Context, tx *sql.Tx) ([]contract.ConfigHTTPCredential, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id,name,host,port,allow_wildcard,header,prefix FROM http_credentials WHERE deleted=0 ORDER BY id LIMIT 257`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []contract.ConfigHTTPCredential{}
	for rows.Next() {
		var c contract.ConfigHTTPCredential
		if err := rows.Scan(&c.Ref, &c.Name, &c.Boundary.Host, &c.Boundary.Port, &c.Boundary.AllowWildcard, &c.Recipe.Header, &c.Recipe.Prefix); err != nil {
			return nil, err
		}
		definition := Definition{Name: c.Name, Boundary: Boundary{Host: c.Boundary.Host, Port: c.Boundary.Port, AllowWildcard: c.Boundary.AllowWildcard}, Recipe: c.Recipe}
		canonical, err := Normalize(definition)
		if err != nil || canonical != definition {
			return nil, ErrInvalid
		}
		out = append(out, c)
	}
	if len(out) > 256 {
		return nil, ErrInvalid
	}
	return out, rows.Err()
}
