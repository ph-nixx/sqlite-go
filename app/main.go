package main

import (
	"encoding/binary"
	"fmt"
	"log"
	"os"
	"strings"
	// Available if you need it!
	// "github.com/xwb1989/sqlparser"
)

type fileField string

const (
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
	tblName string
}

func getFileField(dbfile *os.File, field fileField) (uint16, error) {
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

func getTableNames(dbfile *os.File) ([]string, error) {
	recordHeaders, err := getRecordHeaders(dbfile)
	if err != nil {
		return nil, err
	}
	result := make([]string, len(recordHeaders))
	for i, record := range recordHeaders {
		result[i] = record.tblName
	}
	return result, nil
}

func getCellOffsets(dbfile *os.File) ([]int, error) {
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
		number := binary.BigEndian.Uint16(buf[start:end])
		result[i] = int(number)
	}
	return result, nil
}

func getRecordHeaders(dbfile *os.File) ([]RecordHeader, error) {
	offsets, err := getCellOffsets(dbfile)
	if err != nil {
		return nil, err
	}
	result := make([]RecordHeader, len(offsets))
	for i, cellOffset := range offsets {
		readOffset, err := skipVarintAt(dbfile, cellOffset)
		if err != nil {
			return nil, err
		}
		headerSizeOffset, err := skipVarintAt(dbfile, readOffset)
		if err != nil {
			return nil, err
		}
		readOffset, headerSize, err := parseVarint(dbfile, headerSizeOffset)
		if err != nil {
			return nil, err
		}

		seekOffset := headerSizeOffset + headerSize
		readOffset, typeSize, err := parseVarint(dbfile, readOffset)
		if err != nil {
			return nil, err
		}
		seekOffset += typeSize
		readOffset, nameSize, err := parseVarint(dbfile, readOffset)
		if err != nil {
			return nil, err
		}
		seekOffset += nameSize
		readOffset, tblNameSize, err := parseVarint(dbfile, readOffset)
		if err != nil {
			return nil, err
		}

		buf := make([]byte, tblNameSize)
		if _, err := dbfile.ReadAt(buf, int64(seekOffset)); err != nil {
			return nil, err
		}
		result[i] = RecordHeader{tblName: string(buf)}
	}
	return result, nil
}

func skipVarintAt(dbfile *os.File, offset int) (int, error) {
	buf := make([]byte, varIntMax)
	if _, err := dbfile.ReadAt(buf, int64(offset)); err != nil {
		return 0, err
	}
	for i := range varIntMax - 1 {
		b := buf[i]
		if b>>7 == 0 {
			return offset + i + 1, nil
		}
	}
	return offset + varIntMax, nil
}

func parseVarint(dbfile *os.File, readOffset int) (int, int, error) {
	value, n, err := decodeVarintAt(dbfile, readOffset)
	if err != nil {
		return 0, 0, err
	}
	if value >= 13 && value%2 != 0 {
		return readOffset + n, decodeTextLength(value), nil
	}
	if value >= 12 && value%2 == 0 {
		return readOffset + n, decodeBLOBLength(value), nil
	}
	return readOffset + n, int(value), nil
}

func decodeTextLength(value uint64) int {
	return int((value - 13) / 2)
}

func decodeBLOBLength(value uint64) int {
	return int((value - 12) / 2)
}

func decodeVarintAt(dbfile *os.File, offset int) (uint64, int, error) {
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
