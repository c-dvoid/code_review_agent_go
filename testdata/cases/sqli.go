package store

import (
	"database/sql"
	"fmt"
)

func FindUser(db *sql.DB, name string) (int, error) {
	q := fmt.Sprintf("SELECT id FROM users WHERE name = '%s'", name)
	row := db.QueryRow(q)
	var id int
	err := row.Scan(&id)
	return id, err
}
