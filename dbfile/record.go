package dbfile

import (
	"encoding/binary"
	"fmt"
	"math"
	"strconv"
)

const (
	databaseFilePageSizePtr = 16
	varintMax               = 9
)

type RecordField struct {
	serialType int
	buf        []byte
}

// The on disk representation of a sqlite row: a set of fields with the meta data to collection them.
// Table leaf cell.
type Cell struct {
	fields map[string]*RecordField
}

func (c *Cell) UnwrapAsStrings(colOrder ...string) ([]string, error) {
	fieldValues := make([]string, len(colOrder))
	for i, col := range colOrder {
		str, err := c.UnwrapAsString(col)
		if err != nil {
			return nil, err
		}
		fieldValues[i] = str
	}
	return fieldValues, nil
}

func (c *Cell) UnwrapAsString(fieldName string) (string, error) {
	v, ok := c.fields[fieldName]
	if !ok {
		return "", fmt.Errorf("field '%s' does not exist", fieldName)
	}
	fieldValue, err := v.parseToString()
	if err != nil {
		return "", err
	}
	return fieldValue, nil
}

func (r *RecordField) parseToString() (string, error) {
	bufLen := len(r.buf)
	if r.serialType == 0 && bufLen == 0 {
		return "null", nil
	}
	if r.serialType == 0 && bufLen > 0 {
		num, _ := decodeVarint(r.buf)
		return strconv.FormatInt(int64(num), 10), nil
	}

	if r.serialType >= 13 && r.serialType%2 == 1 {
		return string(r.buf), nil
	}
	if r.serialType >= 12 && r.serialType%2 == 0 {
		return "BLOB", nil
	}
	if r.serialType <= 6 {
		padded := append(make([]byte, 8-bufLen), r.buf...)
		num := binary.BigEndian.Uint64(padded)
		return strconv.FormatInt(int64(num), 10), nil
	}
	if r.serialType == 7 {
		num := math.Float64frombits(binary.BigEndian.Uint64(r.buf))
		return strconv.FormatFloat(num, 'f', -1, 64), nil
	}
	if r.serialType == 8 {
		return "0", nil
	}
	if r.serialType == 9 {
		return "1", nil
	}
	return "", fmt.Errorf("serial type '%d' is undefined", r.serialType)
}

func decodeSerialTypePayloadLen(serialType int) int {
	if serialType >= 13 && serialType%2 != 0 {
		return int((serialType - 13) / 2)
	}
	if serialType >= 12 && serialType%2 == 0 {
		return int((serialType - 12) / 2)
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

func decodeVarint(buf []byte) (int, int) {
	var number uint64
	for i := range varintMax - 1 {
		b := buf[i]
		number = (number << 7) | uint64(b&0x7F)
		if b>>7 == 0 {
			return int(number), i + 1
		}
	}
	number = (number << 8) | uint64(buf[varintMax-1])
	return int(number), varintMax
}
