package dbfile

import (
	"fmt"
	"io"
)

const (
	databaseFilePageSizeOffset       = 16
	cellPtrArraySizeOffsetFromHeader = 3
	cellPtrArrayOffsetFromLeafHeader = 8
	cellPtrVarint                    = 2
	pageSizeVarint                   = 2
	pageCellCountVarint              = 2
	varintMax                        = 9
	sqliteRecordSchemaSize           = 4
	SqliteSchemaPage                 = 1
)

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
