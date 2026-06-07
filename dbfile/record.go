package dbfile

import (
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"strconv"
)

const (
	databaseFilePageSizePtr = 16
	varintMax               = 9
)

type RecordField struct {
	serialType int
	ptr        int
	length     int
}

func (r *RecordField) parseToString(fd io.ReaderAt) (string, error) {
	if r.serialType == 0 {
		return "null", nil
	}

	buf := make([]byte, r.length)
	if _, err := fd.ReadAt(buf, int64(r.ptr)); err != nil {
		return "", err
	}

	if r.serialType >= 13 && r.serialType%2 == 1 {
		return string(buf), nil
	}
	if r.serialType >= 12 && r.serialType%2 == 0 {
		return "BLOB", nil
	}
	if r.serialType <= 6 {
		padded := append(make([]byte, 8-r.length), buf...)
		num := binary.BigEndian.Uint64(padded)
		return strconv.FormatInt(int64(num), 10), nil
	}
	if r.serialType == 7 {
		num := math.Float64frombits(binary.BigEndian.Uint64(buf))
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

func decodeVarintAt(dbfile io.ReaderAt, ptr int) (uint64, int, error) {
	buf := make([]byte, varintMax)
	if _, err := dbfile.ReadAt(buf, int64(ptr)); err != nil {
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
