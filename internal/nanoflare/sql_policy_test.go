package nanoflare

import (
	"errors"
	"testing"
)

func TestSQLPolicyCountsStatementsWithoutLiteralOrCommentConfusion(t *testing.T) {
	for _, test := range []struct {
		sql   string
		count int
	}{
		{`SELECT '; ATTACH ''x''; ' AS text`, 1},
		{"-- ; PRAGMA writable_schema=ON\nSELECT 1; /* ; ATTACH */ SELECT 2;", 2},
		{`CREATE TABLE "semi;colon" ("quoted""field" TEXT); INSERT INTO "semi;colon" VALUES ('a; b')`, 2},
		{`PRAGMA main.table_info('semi;colon'); PRAGMA foreign_keys=ON`, 2},
		{`WITH value AS (SELECT 42) SELECT * FROM value`, 1},
	} {
		count, err := InspectSQL(test.sql)
		if err != nil || count != test.count {
			t.Errorf("%q: count=%d err=%v", test.sql, count, err)
		}
	}
}
func TestSQLPolicyRejectsFilesystemAndAmbiguousPrograms(t *testing.T) {
	for _, sql := range []string{
		`ATTACH DATABASE '/tmp/other.sqlite' AS other`,
		`SELECT 1; /* bypass */ ATTACH DATABASE ? AS other`,
		`EXPLAIN ATTACH '/tmp/other.sqlite' AS other`,
		`VACUUM INTO '/tmp/copied.sqlite'`,
		`PRAGMA writable_schema=ON`, `PRAGMA main."temp_store_directory"='/tmp'`,
		`SELECT * FROM pragma_database_list`, `SELECT * FROM main."pragma_database_list"`, `SELECT * FROM pragma_database_list()`, `SELECT * FROM "pragma_compile_options"()`,
		`SELECT load_extension('/tmp/unsafe')`, `SELECT "load_extension"('/tmp/unsafe')`,
		"SELECT `load_extension`('/tmp/unsafe')", `SELECT [load_extension]('/tmp/unsafe')`,
		`SELECT 'load_extension'('/tmp/unsafe')`, `SELECT * FROM fsdir('/tmp')`,
		`CREATE VIRTUAL TABLE leaked USING csv(filename='/tmp/data.csv')`,
		`CREATE TRIGGER infinite AFTER INSERT ON items BEGIN UPDATE items SET n=n+1; END`,
		`BEGIN; SELECT 1; COMMIT`, `SELECT 'unterminated`, `SELECT 1 /* unterminated`,
	} {
		if _, err := InspectSQL(sql); !errors.Is(err, ErrSQLPolicy) {
			t.Errorf("accepted unsafe SQL %q: %v", sql, err)
		}
	}
}
