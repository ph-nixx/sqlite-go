package dbfile

import (
	"encoding/binary"
	"fmt"
	"github.com/xwb1989/sqlparser"
	"io"
	"strings"
)

const (
	sqliteSchemaPageOffset   = 0
	sqliteSchemaHeaderOffset = 100
	sqliteSchemaTblName      = "sqlite_schema"
)

var sqliteSchema = []string{"tableType", "name", "tblName", "rootpage", "sql"}

// The interface for a SQLite `.db` file:
// providing access to tables, the Database Header, and system tables e.g. `sqlite_schema`.
//
// A instance of this struct shares the same file descriptor with every table it instantiates.
type DBFile struct {
	fd           io.ReaderAt
	SqliteSchema Table
	PageSize     int
}

func New(fd io.ReaderAt) (*DBFile, error) {
	buf := make([]byte, pageSizeVarint)
	if _, err := fd.ReadAt(buf, databaseFilePageSizeOffset); err != nil {
		return nil, err
	}
	pageSize := int(binary.BigEndian.Uint16(buf))
	sqliteSchema := Table{
		fd:         fd,
		tblName:    sqliteSchemaTblName,
		pageOffset: sqliteSchemaPageOffset,
	}
	return &DBFile{fd: fd, SqliteSchema: sqliteSchema, PageSize: pageSize}, nil
}

func (d *DBFile) GetTable(tblName string) (*Table, error) {
	record, err := d.getTableRecordHeader(tblName)
	if err != nil {
		return nil, err
	}

	rootpageInt64, err := record.schema[sqliteSchema[3]].parseInt(d.fd)
	if err != nil {
		return nil, err
	}

	rootpage := int(rootpageInt64)
	pageOffset := (rootpage - 1) * d.PageSize
	colNames, err := getTableColNames(d.fd, record)
	if err != nil {
		return nil, err
	}

	return &Table{
		fd: d.fd, recordHeader: record, tblColNames: colNames, tblName: tblName, pageOffset: pageOffset,
	}, nil
}

func (d *DBFile) GetTableNames() ([]string, error) {
	offsets, err := d.SqliteSchema.getCellPtrs()
	if err != nil {
		return nil, err
	}
	records := make([]RecordHeader, len(offsets))
	for i, cellOffset := range offsets {
		if err := records[i].setHeaderMeta(d.fd, cellOffset); err != nil {
			return nil, err
		}
		if err := records[i].setRecordHeaderFields(d.fd, sqliteSchema); err != nil {
			return nil, err
		}
	}
	result := make([]string, len(records))
	for i, record := range records {
		tblName, err := record.schema[sqliteSchema[2]].parseString(d.fd)
		if err != nil {
			return nil, err
		}
		result[i] = tblName
	}
	return result, nil
}

func (d *DBFile) getTableRecordHeader(tblName string) (*RecordHeader, error) {
	offsets, err := d.SqliteSchema.getCellPtrs()
	if err != nil {
		return nil, err
	}
	for _, offset := range offsets {
		record := &RecordHeader{}
		if err := record.setHeaderMeta(d.fd, offset); err != nil {
			return nil, err
		}
		if err := record.setRecordHeaderFields(d.fd, sqliteSchema); err != nil {
			return nil, err
		}
		if recordTblName, err := record.schema[sqliteSchema[2]].parseString(d.fd); err != nil {
			return nil, err
		} else if recordTblName == tblName {
			return record, nil
		}
	}
	return nil, fmt.Errorf("table '%s' does not exist", tblName)
}

func getTableColNames(fd io.ReaderAt, record *RecordHeader) ([]string, error) {
	tblCreateStatement, err := record.schema[sqliteSchema[4]].parseString(fd)
	if err != nil {
		return nil, err
	}
	stmt, err := sqlparser.Parse(normalizeSql(tblCreateStatement))
	if err != nil {
		return nil, err
	}
	ddl, ok := stmt.(*sqlparser.DDL)
	if !ok {
		return nil, fmt.Errorf("expected a CREATE TABLE statement: %s", sqlparser.String(stmt))
	}
	cols := []string{}
	for _, col := range ddl.TableSpec.Columns {
		cols = append(cols, col.Name.String())
	}
	return cols, nil
}

func normalizeSql(sql string) string {
	return strings.ReplaceAll(sql, " autoincrement", "")
}
