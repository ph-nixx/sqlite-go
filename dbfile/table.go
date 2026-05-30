package dbfile

import (
	"encoding/binary"
	"fmt"
	"io"
)

type Table struct {
	fd           io.ReaderAt
	recordHeader *RecordHeader
	tblColNames  []string
	tblName      string
	pageOffset   int
}

func (t *Table) GetRowFields(cols []string) ([][]string, error) {
	offsets, err := t.getCellPtrs()
	if err != nil {
		return nil, err
	}
	fieldValues := make([][]string, len(offsets))
	for i, offset := range offsets {
		record := RecordHeader{}
		if err := record.setHeaderMeta(t.fd, offset); err != nil {
			return nil, err
		}
		if err := record.setRecordHeaderFields(t.fd, t.tblColNames); err != nil {
			return nil, err
		}
		fields := make([]string, len(cols))
		for j, col := range cols {
			field, ok := record.schema[col]
			if !ok {
				return nil, fmt.Errorf("column '%s' does not exist in table '%s'", col, t.tblName)
			}
			fieldValue, err := field.parseString(t.fd)
			if err != nil {
				return nil, err
			}
			fields[j] = fieldValue
		}
		fieldValues[i] = fields
	}
	return fieldValues, nil
}

func (t *Table) GetCellPtrArray() (int, int, error) {
	buf := make([]byte, pageCellCountVarint)
	cellPtrArraySizeOffset := t.pageOffset + cellPtrArraySizeOffsetFromHeader
	if _, err := t.fd.ReadAt(buf, int64(cellPtrArraySizeOffset)); err != nil {
		return 0, 0, fmt.Errorf("failed to read BTree page header: %w", err)
	}

	// ASSUMING always BTree leaf page
	return t.pageOffset + cellPtrArrayOffsetFromLeafHeader, int(binary.BigEndian.Uint16(buf)), nil
}

func (t *Table) getCellPtrs() ([]int, error) {
	cellPtrArrayOffset, size, err := t.GetCellPtrArray()
	if err != nil {
		return nil, err
	}

	buf := make([]byte, size*cellPtrVarint)
	_, err = t.fd.ReadAt(buf, int64(cellPtrArrayOffset))
	if err != nil {
		return nil, fmt.Errorf("failed to read cell ptr array: %w", err)
	}
	cellPtrs := make([]int, size)
	for i := range size {
		start := i * cellPtrVarint
		end := (i + 1) * cellPtrVarint
		cellPtrs[i] = int(binary.BigEndian.Uint16(buf[start:end])) + t.pageOffset
	}
	return cellPtrs, nil
}
