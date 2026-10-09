package postgres

import (
	"context"
	"fmt"
)

// ServerVersionNum reads PostgreSQL's numeric server version.
func ServerVersionNum(ctx context.Context, c Client) (int, error) {
	var version int
	row, err := c.QueryRow(ctx, "SELECT current_setting('server_version_num')::integer")
	if err != nil {
		return 0, fmt.Errorf("creating PostgreSQL server version query: %w", err)
	}
	if err := row.Scan(&version); err != nil {
		return 0, fmt.Errorf("reading PostgreSQL server version: %w", err)
	}
	return version, nil
}

// FormatServerVersion formats server_version_num using PostgreSQL's version
// encoding, which changed with PostgreSQL 10.
func FormatServerVersion(version int) string {
	if version >= 100000 {
		return fmt.Sprintf("%d.%d", version/10000, version%100)
	}

	return fmt.Sprintf("%d.%d.%d", version/10000, (version/100)%100, version%100)
}
