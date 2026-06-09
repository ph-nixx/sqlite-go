package dbfile

import (
	"encoding/binary"
	"fmt"
	"github.com/xwb1989/sqlparser"
	"io"
	"strconv"
	"strings"
)

const (
	sqliteSchemaPageNum = 1
	pageSizeLen         = 2
)

var sqliteSchemaColOrder = []string{"tableType", "name", "tblName", "rootpage", "sql"}

// The interface for a SQLite `.db` file:
// providing access to tables, the Database Header, and system tables (e.g. sqlite_schema).
//
// A instance of this struct shares the same file descriptor with every table it instantiates.
//
// DBFile is reponsible encapsulating how to operate on sqlite_schema via [DBFile.sqliteSchema].
// This means all sqlite_schema operational knowlege should ideally be confined to DBFile.
type DBFile struct {
	fd           io.ReaderAt
	sqliteSchema Table
	PageSize     int
}

func New(fd io.ReaderAt) (*DBFile, error) {
	buf := make([]byte, pageSizeLen)
	if _, err := fd.ReadAt(buf, databaseFilePageSizePtr); err != nil {
		return nil, err
	}
	pageSize := int(binary.BigEndian.Uint16(buf))
	sqliteSchema := Table{
		fd:       fd,
		ColOrder: sqliteSchemaColOrder,
		rootpage: sqliteSchemaPageNum,
		pageSize: pageSize,
	}
	return &DBFile{fd: fd, sqliteSchema: sqliteSchema, PageSize: pageSize}, nil
}

func (d *DBFile) GetTableNames() ([]string, error) {
	cells, err := d.sqliteSchema.GetLeafCells(nil, sqliteSchemaColOrder[2])
	if err != nil {
		return nil, err
	}
	result := []string{}
	for _, cell := range cells {
		fieldValues, err := cell.UnwrapAsStrings(sqliteSchemaColOrder[2])
		if err != nil {
			return nil, err
		}
		if len(fieldValues) != 1 {
			return nil, fmt.Errorf("sqlite_schema returned more than one value for column '%s'", sqliteSchemaColOrder[2])
		}
		result = append(result, fieldValues[0])
	}
	return result, nil
}

func (d *DBFile) GetTable(tblName string) (*Table, error) {
	con := &Condition{
		Eq: func(cell *Cell) (bool, error) {
			str, err := cell.UnwrapAsString(sqliteSchemaColOrder[2])
			if err != nil {
				return false, err
			}
			return str == tblName, nil
		},
		Cols: sqliteSchemaColOrder[2:3],
	}
	cells, err := d.sqliteSchema.GetLeafCells(
		con,
		sqliteSchemaColOrder[2],
		sqliteSchemaColOrder[3],
		sqliteSchemaColOrder[4],
	)
	if err != nil {
		return nil, err
	}
	if len(cells) == 0 {
		return nil, fmt.Errorf("unknown table '%s'", tblName)
	}
	if len(cells) > 1 {
		return nil, fmt.Errorf("sqlite_schema returned multiple tables named '%s'", tblName)
	}

	tblCreateStmt, err := cells[0].UnwrapAsString(sqliteSchemaColOrder[4])
	if err != nil {
		return nil, err
	}
	stmt, err := sqlparser.Parse(normalizeSql(tblCreateStmt))
	if err != nil {
		return nil, err
	}

	ddl, ok := stmt.(*sqlparser.DDL)
	if !ok {
		return nil, fmt.Errorf("expected a CREATE TABLE statement: %s", sqlparser.String(stmt))
	}
	colNames := []string{}
	for _, col := range ddl.TableSpec.Columns {
		colNames = append(colNames, col.Name.String())
	}

	rootpage, err := cells[0].UnwrapAsString(sqliteSchemaColOrder[3])
	if err != nil {
		return nil, err
	}
	rootpageInt, err := strconv.Atoi(rootpage)
	if err != nil {
		return nil, err
	}

	con = &Condition{
		Eq: func(cell *Cell) (bool, error) {
			name, err := cell.UnwrapAsString(sqliteSchemaColOrder[2])
			if err != nil {
				return false, err
			}

			type_, err := cell.UnwrapAsString(sqliteSchemaColOrder[0])
			if err != nil {
				return false, err
			}
			return name == tblName && type_ == "index", nil
		},
		Cols: []string{sqliteSchemaColOrder[0], sqliteSchemaColOrder[2]},
	}

	cells, err = d.sqliteSchema.GetLeafCells(con, sqliteSchemaColOrder[3:]...)
	if err != nil {
		return nil, err
	}

	indices := map[string]*index{}
	for _, c := range cells {
		sql, err := c.UnwrapAsString(sqliteSchemaColOrder[4])
		if err != nil {
			return nil, err
		}
		stmt, err := sqlparser.Parse(sql)
		if err != nil {
			panic(err)
		}

		ddl, ok := stmt.(*sqlparser.DDL)
		if !ok {
			panic("not a DDL")
		}

		if ddl.TableSpec == nil {
			panic("no table spec")
		}

		for _, inx := range ddl.TableSpec.Indexes {
			if len(inx.Columns) != 1 {
				return nil, fmt.Errorf("[Table.indices] only supports single column indices")
			}
			col := inx.Columns[0].Column.String()
			pageNum, err := c.UnwrapAsString(sqliteSchemaColOrder[3])
			if err != nil {
				return nil, err
			}

			num, err := strconv.Atoi(pageNum)
			if err != nil {
				return nil, err
			}
			indices[col] = &index{pageNum: num, colOrder: []string{col, indexRowIdField}}
		}
	}

	tbl := &Table{
		fd:       d.fd,
		ColOrder: colNames,
		rootpage: rootpageInt,
		pageSize: d.PageSize,
		indices:  indices,
	}
	return tbl, nil
}

func normalizeSql(sql string) string {
	sql = strings.ReplaceAll(sql, " autoincrement", "")
	sql = strings.ReplaceAll(sql, `"`, "`")
	return sql
}
