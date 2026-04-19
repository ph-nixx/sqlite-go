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
	cellOffset          int
	headerSize          int
	headerOffset        int
	typeVarintOffset    int
	typeSize            int
	typeOffset          int
	nameVarintOffset    int
	nameSize            int
	nameOffset          int
	tblNameVarintOffset int
	tblName             []byte
	tblNameSize         int
	tblNameOffset       int
}

func (r *RecordHeader) setHeaderMeta(dbfile *os.File, cellOffset int) error {
	r.cellOffset = cellOffset
	_, n1, err := decodeVarintAt(dbfile, cellOffset)
	if err != nil {
		return fmt.Errorf("failed to decode recordSize varint from %s : %w", dbfile.Name(), err)
	}
	_, n2, err := decodeVarintAt(dbfile, cellOffset+n1)
	if err != nil {
		return fmt.Errorf("failed to decode rowid varint from %s : %w", dbfile.Name(), err)
	}

	r.headerOffset = cellOffset + n1 + n2
	headerSize, n3, err := decodeVarintAt(dbfile, r.headerOffset)
	if err != nil {
		return fmt.Errorf("failed to decode record header size varint from %s : %w", dbfile.Name(), err)
	}
	r.headerSize = int(headerSize)
	r.typeVarintOffset = r.headerOffset + n3
	return nil
}

func (r *RecordHeader) setTypeMeta(dbfile *os.File, headerOffset, headerSize int) error {
	size, n, err := parseSerialTypeVarintAt(dbfile, r.typeVarintOffset)
	if err != nil {
		return fmt.Errorf("failed to parse record type field from %s : %w", dbfile.Name(), err)
	}
	r.typeOffset = headerOffset + headerSize
	r.typeSize = size
	r.nameVarintOffset = r.typeVarintOffset + n
	return nil
}

func (r *RecordHeader) setNameMeta(dbfile *os.File, typeOffset, typeSize int) error {
	size, n, err := parseSerialTypeVarintAt(dbfile, r.nameVarintOffset)
	if err != nil {
		return fmt.Errorf("failed to parse record name field from %s : %w", dbfile.Name(), err)
	}
	r.nameOffset = typeOffset + typeSize
	r.nameSize = size
	r.tblNameVarintOffset = r.nameVarintOffset + n
	return nil
}

func (r *RecordHeader) setTblName(dbfile *os.File, nameOffset, nameSize int) error {
	size, _, err := parseSerialTypeVarintAt(dbfile, r.tblNameVarintOffset)
	if err != nil {
		return err
	}
	r.tblNameOffset = nameOffset + nameSize
	r.tblNameSize = size
	buf := make([]byte, size)
	if _, err := dbfile.ReadAt(buf, int64(r.tblNameOffset)); err != nil {
		return err
	}
	r.tblName = buf
	return nil
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
		result[i] = string(record.tblName)
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
		result[i] = int(binary.BigEndian.Uint16(buf[start:end]))
	}
	return result, nil
}

func getRecordHeaders(dbfile *os.File) ([]RecordHeader, error) {
	offsets, err := getCellOffsets(dbfile)
	if err != nil {
		return nil, err
	}
	records := make([]RecordHeader, len(offsets))
	for i, cellOffset := range offsets {
		if err := records[i].setHeaderMeta(dbfile, cellOffset); err != nil {
			return nil, err
		}
		if err := records[i].setTypeMeta(dbfile, records[i].headerOffset, records[i].headerSize); err != nil {
			return nil, err
		}
		if err := records[i].setNameMeta(dbfile, records[i].typeOffset, records[i].typeSize); err != nil {
			return nil, err
		}
		if err := records[i].setTblName(dbfile, records[i].nameOffset, records[i].nameSize); err != nil {
			return nil, err
		}
	}
	return records, nil
}

func parseSerialTypeVarintAt(dbfile *os.File, offset int) (int, int, error) {
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
