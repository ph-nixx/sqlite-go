package main

import (
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	// Available if you need it!
	// "github.com/xwb1989/sqlparser"
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

const (
	tableType = iota
	name
	tblName
	rootpage
)

type RecordHeader struct {
	cellOffset   int
	headerSize   int
	headerOffset int
	schemaOffset int
	schema       []RecordField
}

type RecordField struct {
	serialTypeOffset int
	offset           int
	length           int
	text             []byte
	varint           int
}

func (r *RecordHeader) setHeaderMeta(dbfile io.ReaderAt, cellOffset int) error {
	r.cellOffset = cellOffset
	_, n1, err := decodeVarintAt(dbfile, cellOffset)
	if err != nil {
		return fmt.Errorf("failed to decode recordSize varint: %w", err)
	}
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

func (r *RecordHeader) setSqliteSchemaRecordHeader(dbfile io.ReaderAt) error {
	serialTypeOffset := r.schemaOffset
	offset := r.headerOffset + r.headerSize
	schema := make([]RecordField, sqliteRecordSchemaSize)
	for i := range sqliteRecordSchemaSize {
		schema[i].serialTypeOffset = serialTypeOffset
		schema[i].offset = offset
		value, n, err := parseSerialTypeVarintAt(dbfile, serialTypeOffset) // value is to vague and this interface is ambiguouse
		if err != nil {
			return fmt.Errorf("failed to parse record text field: %w", err)
		}
		switch i {
		case tblName:
			buf := make([]byte, value)
			if _, err := dbfile.ReadAt(buf, int64(schema[tblName].offset)); err != nil {
				return err
			}
			schema[i].length = value
			schema[i].text = buf
		case rootpage:
			buf := make([]byte, 1) // brittle
			if _, err := dbfile.ReadAt(buf, int64(schema[rootpage].offset)); err != nil {
				return err
			}
			schema[i].varint = int(buf[0])
			value = n
		}
		offset += value
		serialTypeOffset += n
	}
	r.schema = schema
	return nil
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
		if err := record.setSqliteSchemaRecordHeader(dbfile); err != nil {
			return 0, err
		}
		if string(record.schema[tblName].text) == name {
			return record.schema[rootpage].varint, nil
		}
	}
	return 0, fmt.Errorf("unknown table: %s", name)
}

func getTableNames(dbfile io.ReaderAt) ([]string, error) {
	recordHeaders, err := getSqliteSchemaRecordHeaders(dbfile)
	if err != nil {
		return nil, err
	}
	result := make([]string, len(recordHeaders))
	for i, record := range recordHeaders {
		result[i] = string(record.schema[tblName].text)
	}
	return result, nil
}

func getDBPageSize(dbfile io.ReaderAt) (int, error) {
	buf := make([]byte, pageSizeVarint)
	if _, err := dbfile.ReadAt(buf, databaseFilePageSizeOffset); err != nil {
		return 0, err
	}
	return int(binary.BigEndian.Uint16(buf)), nil
}

func getPageCellPtrArray(dbfile io.ReaderAt, pageNumber int) (int, int, error) {
	pageSize, err := getDBPageSize(dbfile)
	if err != nil {
		return 0, 0, err
	}
	buf := make([]byte, pageCellCountVarint)
	pageOffset := (pageNumber - 1) * pageSize
	if pageNumber == sqliteSchemaPage {
		pageOffset = sqliteSchemaHeaderOffset
	}
	cellPtrArraySizeOffset := pageOffset + cellPtrArraySizeOffsetFromHeader
	if _, err := dbfile.ReadAt(buf, int64(cellPtrArraySizeOffset)); err != nil {
		return 0, 0, fmt.Errorf("failed to read b-tree page header: %w", err)
	}

	// assuming never interior b-tree page
	cellPtrArrayOffset := pageOffset + cellPtrArrayOffsetFromLeafHeader
	return cellPtrArrayOffset, int(binary.BigEndian.Uint16(buf)), nil
}

func getPageCellPtrs(dbfile io.ReaderAt, pageNumber int) ([]int, error) {
	cellPtrArrayOffset, size, err := getPageCellPtrArray(dbfile, pageNumber)
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
		cellPtrs[i] = int(binary.BigEndian.Uint16(buf[start:end]))
	}
	return cellPtrs, nil
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
		if err := records[i].setSqliteSchemaRecordHeader(dbfile); err != nil {
			return nil, err
		}
	}
	return records, nil
}

func parseSerialTypeVarintAt(dbfile io.ReaderAt, offset int) (int, int, error) {
	value, n, err := decodeVarintAt(dbfile, offset)
	if err != nil {
		return 0, 0, err
	}
	if value >= 13 && value%2 != 0 {
		return decodeTextLength(value), n, nil
	}
	if value >= 12 && value%2 == 0 {
		return decodeBLOBLength(value), n, nil
	}
	return int(value), n, nil
}

func decodeTextLength(value uint64) int {
	return int((value - 13) / 2)
}

func decodeBLOBLength(value uint64) int {
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

func dotCommand(databaseFilePath, command string) {
	switch command {
	case ".dbinfo":
		fd, err := os.Open(databaseFilePath)
		if err != nil {
			log.Fatalln(err)
		}
		defer fd.Close()
		pageSize, err := getDBPageSize(fd)
		if err != nil {
			log.Fatalln(err)
		}
		_, cellCount, err := getPageCellPtrArray(fd, sqliteSchemaPage)
		if err != nil {
			log.Fatalln(err)
		}

		fmt.Printf("database page size: %v\n", pageSize)
		fmt.Printf("number of tables: %v\n", cellCount)
	case ".tables":
		fd, err := os.Open(databaseFilePath)
		if err != nil {
			log.Fatalln(err)
		}
		defer fd.Close()
		names, err := getTableNames(fd)
		if err != nil {
			log.Fatalln(err)
		}

		fmt.Println(strings.Join(names, " "))
	default:
		log.Fatalln("unknown command", command)
	}
}

// Usage: your_program.sh sample.db .dbinfo
func main() {
	databaseFilePath := os.Args[1]
	command := os.Args[2]

	if len(os.Args) == 3 && strings.HasPrefix(command, ".") {
		dotCommand(databaseFilePath, command)
		return
	}

	sql := strings.Split(strings.TrimSpace(command), " ")
	if len(sql) < 4 {
		log.Fatalf("invalid sql syntax: %s\n", strings.Join(sql, " "))
	}

	switch strings.ToUpper(sql[0]) {
	case "SELECT":
		fd, err := os.Open(databaseFilePath)
		if err != nil {
			log.Fatal(err)
		}
		defer fd.Close()
		if strings.ToUpper(sql[1]) == "COUNT(*)" {
			page, err := getTableRootpage(fd, sql[3])
			if err != nil {
				log.Fatalln(err)
			}
			_, cellCount, err := getPageCellPtrArray(fd, page)
			if err != nil {
				log.Fatalln(err)
			}
			fmt.Println(cellCount)
		} else {
			log.Fatalf("invalid SELECT statement: %s\n", strings.Join(sql[:3], " "))
		}
	default:
		log.Fatalf("unknown DML statement: %s\n", sql[0])
	}
}
