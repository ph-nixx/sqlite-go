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
	recordSchemaSize             = 3
	pageSize           fileField = "pageSize"
	cellCount          fileField = "cellCount"
	mainHeaderOffset             = 0
	schemaHeaderOffset           = 100
	cellArrayOffset              = 108
	pageSizeOffset               = 16
	cellCountOffset              = 3
	pageSizeBytes                = 2
	cellCountBytes               = 2
	cellOffsetBytes              = 2
	varIntMax                    = 9
)

type RecordHeader struct {
	cellOffset   int
	headerSize   int
	headerOffset int
	schemaOffset int
	schema       []RecordTextField
}

const (
	fileType = iota
	fieldName
	fieldTblName
)

type RecordTextField struct {
	serialTypeOffset int
	offset           int
	length           int
	text             []byte
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

func (r *RecordHeader) setSchema(dbfile io.ReaderAt) error {
	serialTypeOffset := r.schemaOffset
	offset := r.headerOffset + r.headerSize
	schema := make([]RecordTextField, recordSchemaSize)
	for i := range recordSchemaSize {
		schema[i].serialTypeOffset = serialTypeOffset
		schema[i].offset = offset
		size, n, err := parseSerialTypeVarintAt(dbfile, serialTypeOffset)
		if err != nil {
			return fmt.Errorf("failed to parse record text field: %w", err)
		}
		if i == fieldTblName {
			buf := make([]byte, size)
			if _, err := dbfile.ReadAt(buf, int64(schema[fieldTblName].offset)); err != nil {
				return err
			}
			schema[i].text = buf
		}
		schema[i].length = size
		serialTypeOffset += n
		offset += size
	}
	r.schema = schema
	return nil
}

func getFileField(dbfile io.ReaderAt, field fileField) (uint16, error) {
	readSize := 0
	offset := 0
	switch field {
	case pageSize:
		readSize = pageSizeBytes
		offset = mainHeaderOffset + pageSizeOffset
	case cellCount:
		readSize = cellCountBytes
		offset = schemaHeaderOffset + cellCountOffset
	}

	buf := make([]byte, readSize)
	_, err := dbfile.ReadAt(buf, int64(offset))
	if err != nil {
		return 0, fmt.Errorf("failed to read sqlite file header: %w", err)
	}

	number := binary.BigEndian.Uint16(buf)
	return number, nil
}

func getTableNames(dbfile io.ReaderAt) ([]string, error) {
	recordHeaders, err := getRecordHeaders(dbfile)
	if err != nil {
		return nil, err
	}
	result := make([]string, len(recordHeaders))
	for i, record := range recordHeaders {
		result[i] = string(record.schema[fieldTblName].text)
	}
	return result, nil
}

func getCellOffsets(dbfile io.ReaderAt) ([]int, error) {
	arrayLen, err := getFileField(dbfile, cellCount)
	buf := make([]byte, arrayLen*cellOffsetBytes)
	_, err = dbfile.ReadAt(buf, cellArrayOffset)
	if err != nil {
		return nil, fmt.Errorf("failed to read cell offset array: %w", err)
	}
	result := make([]int, arrayLen)
	for i := range arrayLen {
		start := i * cellOffsetBytes
		end := start + cellOffsetBytes
		result[i] = int(binary.BigEndian.Uint16(buf[start:end]))
	}
	return result, nil
}

func getRecordHeaders(dbfile io.ReaderAt) ([]RecordHeader, error) {
	offsets, err := getCellOffsets(dbfile)
	if err != nil {
		return nil, err
	}
	records := make([]RecordHeader, len(offsets))
	for i, cellOffset := range offsets {
		if err := records[i].setHeaderMeta(dbfile, cellOffset); err != nil {
			return nil, err
		}
		if err := records[i].setSchema(dbfile); err != nil {
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
	buf := make([]byte, varIntMax)
	if _, err := dbfile.ReadAt(buf, int64(offset)); err != nil {
		return 0, 0, err
	}
	var number uint64
	for i := range varIntMax - 1 {
		b := buf[i]
		number = (number << 7) | uint64(b&0x7F)
		if b>>7 == 0 {
			return number, i + 1, nil
		}
	}
	number = (number << 8) | uint64(buf[varIntMax-1])
	return number, varIntMax, nil
}

// Usage: your_program.sh sample.db .dbinfo
func main() {
	databaseFilePath := os.Args[1]
	command := os.Args[2]

	switch command {
	case ".dbinfo":
		fd, err := os.Open(databaseFilePath)
		if err != nil {
			log.Fatal(err)
		}
		defer fd.Close()
		pageSize, err := getFileField(fd, pageSize)
		if err != nil {
			log.Fatalln(err)
		}
		cellCount, err := getFileField(fd, cellCount)
		if err != nil {
			log.Fatalln(err)
		}

		fmt.Printf("database page size: %v\n", pageSize)
		fmt.Printf("number of tables: %v\n", cellCount)
	case ".tables":
		fd, err := os.Open(databaseFilePath)
		if err != nil {
			log.Fatal(err)
		}
		defer fd.Close()
		names, err := getTableNames(fd)
		if err != nil {
			log.Fatalln(err)
		}

		fmt.Println(strings.Join(names, " "))
	default:
		log.Fatalln("Unknown command", command)
		os.Exit(1)
	}
}
