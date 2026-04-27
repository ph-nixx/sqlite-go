package main

import (
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"os"
	"strings"

	// Available if you need it!
	"github.com/xwb1989/sqlparser"
)

type fileField string

const (
	databaseFilePageSizeOffset       = 16
	sqliteSchemaHeaderOffset         = 100
	cellPtrArraySizeOffsetFromHeader = 3
	cellPtrArrayOffsetFromLeafHeader = 8
	cellPtrVarint                    = 2
	pageSizeVarint                   = 2
	pageCellCountVarint              = 2
	varintMax                        = 9
	sqliteRecordSchemaSize           = 4
	sqliteSchemaPage                 = 1
)

var sqliteSchema = []string{"tableType", "name", "tblName", "rootpage", "sql"}

type RecordHeader struct {
	cellOffset   int
	rowidOffset  int
	headerSize   int
	headerOffset int
	schemaOffset int
	schema       map[string]RecordField
}

type RecordField struct {
	offset int
	length int
}

func (r RecordField) parseString(dbfile io.ReaderAt) (string, error) {
	buf := make([]byte, r.length)
	if _, err := dbfile.ReadAt(buf, int64(r.offset)); err != nil {
		return "", err
	}
	return string(buf), nil
}

func (r RecordField) parseInt(dbfile io.ReaderAt) (int64, error) {
	if r.length > 8 {
		return 0, fmt.Errorf("expected RecordField integer value to be at most 8 bytes: %#v", r)
	}
	buf := make([]byte, r.length)
	if _, err := dbfile.ReadAt(buf, int64(r.offset)); err != nil {
		return 0, err
	}
	var num int64
	for _, b := range buf {
		num = num<<8 | int64(b)
	}
	return num, nil
}

func (r *RecordHeader) setHeaderMeta(dbfile io.ReaderAt, cellOffset int) error {
	r.cellOffset = cellOffset
	_, n1, err := decodeVarintAt(dbfile, cellOffset)
	if err != nil {
		return fmt.Errorf("failed to decode recordSize varint: %w", err)
	}
	r.rowidOffset = cellOffset + n1
	_, n2, err := decodeVarintAt(dbfile, cellOffset+n1)
	if err != nil {
		return fmt.Errorf("failed to decode rowid varint: %w", err)
	}
	r.headerOffset = cellOffset + n1 + n2
	headerSize, n3, err := decodeVarintAt(dbfile, r.headerOffset)
	if err != nil {
		return fmt.Errorf("failed to decode record header size varint: %w", err)
	}
	r.headerSize = int(headerSize)
	r.schemaOffset = r.headerOffset + n3
	return nil
}

func (r *RecordHeader) setRecordHeaderFields(dbfile io.ReaderAt, cols []string) error {
	serialTypeOffset := r.schemaOffset
	offset := r.headerOffset + r.headerSize
	schema := map[string]RecordField{}
	for _, name := range cols {
		field := RecordField{}
		serialType, n, err := decodeVarintAt(dbfile, serialTypeOffset) // value is to vague and this interface is ambiguouse
		if err != nil {
			return fmt.Errorf("failed to parse record text field: %w", err)
		}

		payloadLen := decodeSerialTypePayloadLen(int(serialType))
		if name == "id" && serialType == 0 {
			field.offset = r.rowidOffset
		} else {
			field.offset = offset
			field.length = payloadLen
		}
		schema[name] = field

		offset += payloadLen
		serialTypeOffset += n
	}
	r.schema = schema
	return nil
}

func getRowFields(dbfile io.ReaderAt, tblName string, cols []string) ([][]string, error) {
	rootpage, err := getTableRootpage(dbfile, tblName)
	if err != nil {
		return nil, err
	}
	offsets, err := getPageCellPtrs(dbfile, rootpage)
	if err != nil {
		return nil, err
	}
	schemaCols, err := getTableColNames(dbfile, tblName)
	if err != nil {
		return nil, err
	}
	fieldValues := make([][]string, len(offsets))
	for i, offset := range offsets {
		record := RecordHeader{}
		if err := record.setHeaderMeta(dbfile, offset); err != nil {
			return nil, err
		}
		if err := record.setRecordHeaderFields(dbfile, schemaCols); err != nil {
			return nil, err
		}
		fields := make([]string, len(cols))
		for j, col := range cols {
			field, ok := record.schema[col]
			if !ok {
				return nil, fmt.Errorf("column '%s' does not exist in table '%s'", col, tblName)
			}
			fieldValue, err := field.parseString(dbfile)
			if err != nil {
				return nil, err
			}
			fields[j] = fieldValue
		}
		fieldValues[i] = fields
	}
	return fieldValues, nil
}

func getTableColNames(dbfile io.ReaderAt, tblName string) ([]string, error) {
	record, err := getTableRecordHeader(dbfile, tblName)
	if err != nil {
		return nil, err
	}
	tblCreateStatement, err := record.schema[sqliteSchema[4]].parseString(dbfile)
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

func getTableRecordHeader(dbfile io.ReaderAt, tblName string) (*RecordHeader, error) {
	offsets, err := getPageCellPtrs(dbfile, sqliteSchemaPage)
	if err != nil {
		return nil, err
	}
	for _, offset := range offsets {
		record := &RecordHeader{}
		if err := record.setHeaderMeta(dbfile, offset); err != nil {
			return nil, err
		}
		if err := record.setRecordHeaderFields(dbfile, sqliteSchema); err != nil {
			return nil, err
		}
		if recordTblName, err := record.schema[sqliteSchema[2]].parseString(dbfile); err != nil {
			return nil, err
		} else if recordTblName == tblName {
			return record, nil
		}
	}
	return nil, fmt.Errorf("unknown table %s", tblName)
}

func getTableNames(dbfile io.ReaderAt) ([]string, error) {
	recordHeaders, err := getSqliteSchemaRecordHeaders(dbfile)
	if err != nil {
		return nil, err
	}
	result := make([]string, len(recordHeaders))
	for i, record := range recordHeaders {
		tblName, err := record.schema[sqliteSchema[2]].parseString(dbfile)
		if err != nil {
			return nil, err
		}
		result[i] = tblName
	}
	return result, nil
}

func getSqliteSchemaRecordHeaders(dbfile io.ReaderAt) ([]RecordHeader, error) {
	offsets, err := getPageCellPtrs(dbfile, sqliteSchemaPage)
	if err != nil {
		return nil, err
	}
	records := make([]RecordHeader, len(offsets))
	for i, cellOffset := range offsets {
		if err := records[i].setHeaderMeta(dbfile, cellOffset); err != nil {
			return nil, err
		}
		if err := records[i].setRecordHeaderFields(dbfile, sqliteSchema); err != nil {
			return nil, err
		}
	}
	return records, nil
}

func getTableRootpage(dbfile io.ReaderAt, name string) (int, error) {
	offsets, err := getPageCellPtrs(dbfile, sqliteSchemaPage)
	if err != nil {
		return 0, err
	}
	for _, cellOffset := range offsets {
		record := RecordHeader{}
		if err := record.setHeaderMeta(dbfile, cellOffset); err != nil {
			return 0, err
		}
		if err := record.setRecordHeaderFields(dbfile, sqliteSchema); err != nil {
			return 0, err
		}
		if tblName, err := record.schema[sqliteSchema[2]].parseString(dbfile); err != nil {
			return 0, err
		} else if tblName != name {
			continue
		}
		rootpage, err := record.schema[sqliteSchema[3]].parseInt(dbfile)
		if err != nil {
			return 0, err
		}
		return int(rootpage), nil
	}
	return 0, fmt.Errorf("unknown table: %s", name)
}

func getDBPageSize(dbfile io.ReaderAt) (int, error) {
	buf := make([]byte, pageSizeVarint)
	if _, err := dbfile.ReadAt(buf, databaseFilePageSizeOffset); err != nil {
		return 0, err
	}
	return int(binary.BigEndian.Uint16(buf)), nil
}

func getPageOffset(dbfile io.ReaderAt, pageNumber int) (int, error) {
	pageSize, err := getDBPageSize(dbfile)
	if err != nil {
		return 0, err
	}
	return (pageNumber - 1) * pageSize, nil
}

func getPageCellPtrArray(dbfile io.ReaderAt, pageOffset int) (int, int, error) {
	if pageOffset == 0 {
		pageOffset = sqliteSchemaHeaderOffset
	}
	buf := make([]byte, pageCellCountVarint)
	cellPtrArraySizeOffset := pageOffset + cellPtrArraySizeOffsetFromHeader
	if _, err := dbfile.ReadAt(buf, int64(cellPtrArraySizeOffset)); err != nil {
		return 0, 0, fmt.Errorf("failed to read BTree page header: %w", err)
	}

	// ASSUMING always BTree leaf page
	return pageOffset + cellPtrArrayOffsetFromLeafHeader, int(binary.BigEndian.Uint16(buf)), nil
}

func getPageCellPtrs(dbfile io.ReaderAt, pageNumber int) ([]int, error) {
	pageOffset, err := getPageOffset(dbfile, pageNumber)
	if err != nil {
		return nil, err
	}
	cellPtrArrayOffset, size, err := getPageCellPtrArray(dbfile, pageOffset)
	if err != nil {
		return nil, err
	}

	buf := make([]byte, size*cellPtrVarint)
	_, err = dbfile.ReadAt(buf, int64(cellPtrArrayOffset))
	if err != nil {
		return nil, fmt.Errorf("failed to read cell ptr array: %w", err)
	}
	cellPtrs := make([]int, size)
	for i := range size {
		start := i * cellPtrVarint
		end := (i + 1) * cellPtrVarint
		cellPtrs[i] = int(binary.BigEndian.Uint16(buf[start:end])) + pageOffset
	}
	return cellPtrs, nil
}

func decodeSerialTypePayloadLen(serialType int) int {
	if serialType >= 13 && serialType%2 != 0 {
		return decodeTextLength(serialType)
	}
	if serialType >= 12 && serialType%2 == 0 {
		return decodeBLOBLength(serialType)
	}
	switch serialType {
	case 5:
		return 6
	case 6, 7:
		return 8
	case 8, 9:
		return 0
	default:
		return serialType
	}
}

func decodeTextLength(value int) int {
	return int((value - 13) / 2)
}

func decodeBLOBLength(value int) int {
	return int((value - 12) / 2)
}

func decodeVarintAt(dbfile io.ReaderAt, offset int) (uint64, int, error) {
	buf := make([]byte, varintMax)
	if _, err := dbfile.ReadAt(buf, int64(offset)); err != nil {
		return 0, 0, err
	}
	var number uint64
	for i := range varintMax - 1 {
		b := buf[i]
		number = (number << 7) | uint64(b&0x7F)
		if b>>7 == 0 {
			return number, i + 1, nil
		}
	}
	number = (number << 8) | uint64(buf[varintMax-1])
	return number, varintMax, nil
}

func dotCommand(fd *os.File, command string) {
	switch command {
	case ".dbinfo":
		pageSize, err := getDBPageSize(fd)
		if err != nil {
			log.Fatalln(err)
		}
		pageOffset, err := getPageOffset(fd, sqliteSchemaPage)
		if err != nil {
			log.Fatalln(err)
		}
		_, cellCount, err := getPageCellPtrArray(fd, pageOffset)
		if err != nil {
			log.Fatalln(err)
		}

		fmt.Printf("database page size: %v\n", pageSize)
		fmt.Printf("number of tables: %v\n", cellCount)
	case ".tables":
		names, err := getTableNames(fd)
		if err != nil {
			log.Fatalln(err)
		}

		fmt.Println(strings.Join(names, " "))
	default:
		log.Fatalln("unknown command", command)
	}
}

func isCount(exprs sqlparser.SelectExprs) bool {
	if len(exprs) != 1 {
		return false
	}
	fn, ok := exprs[0].(*sqlparser.AliasedExpr).Expr.(*sqlparser.FuncExpr)
	if !ok {
		return false
	}
	if !fn.IsAggregate() {
		return false
	}
	return fn.Name.EqualString("count")
}

func isSingleTable(tableExprs sqlparser.TableExprs) bool {
	return len(tableExprs) == 1
}

// Usage: your_program.sh sample.db .dbinfo
func main() {
	databaseFilePath := os.Args[1]
	command := os.Args[2]

	fd, err := os.Open(databaseFilePath)
	if err != nil {
		log.Fatalln(err)
	}
	defer fd.Close()

	if len(os.Args) == 3 && strings.HasPrefix(command, ".") {
		dotCommand(fd, command)
		return
	}

	stmt, err := sqlparser.Parse(command)
	if err != nil {
		log.Fatalln(err)
	}

	switch stmt := stmt.(type) {
	case *sqlparser.Select:
		if isCount(stmt.SelectExprs) {
			firstTable := stmt.From[0].(*sqlparser.AliasedTableExpr)
			tbl := firstTable.Expr.(sqlparser.TableName)
			rootpage, err := getTableRootpage(fd, tbl.Name.String())
			if err != nil {
				log.Fatalln(err)
			}
			pageOffset, err := getPageOffset(fd, rootpage)
			if err != nil {
				log.Fatalln(err)
			}
			_, cellCount, err := getPageCellPtrArray(fd, pageOffset)
			if err != nil {
				log.Fatalln(err)
			}
			fmt.Println(cellCount)
		} else if isSingleTable(stmt.From) {
			firstTable := stmt.From[0].(*sqlparser.AliasedTableExpr)
			tbl := firstTable.Expr.(sqlparser.TableName)
			cols := strings.Split(sqlparser.String(stmt.SelectExprs), ", ")
			rows, err := getRowFields(fd, tbl.Name.String(), cols)
			if err != nil {
				log.Fatalln(err)
			}
			for _, row := range rows {
				fmt.Println(strings.Join(row, "|"))
			}
		}
	}
}
