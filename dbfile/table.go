package dbfile

import (
	"encoding/binary"
	"fmt"
	"io"
	"slices"
	"strings"
)

type Table struct {
	fd         io.ReaderAt
	ColOrder   []string
	pageOffset int
}

// The on disk representation of a sqlite row: a set of fields with the meta data to collection them.
type Cell struct {
	fd     io.ReaderAt
	fields map[string]*RecordField
}

type Condition func(*Cell) (bool, error)

func (c *Cell) UnwrapAsStrings(colOrder ...string) ([]string, error) {
	if len(colOrder) != len(c.fields) {
		return nil, fmt.Errorf(
			"'%d' columns provided expected '%d'",
			len(colOrder),
			len(c.fields),
		)
	}

	fieldValues := make([]string, len(c.fields))
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
	fieldValue, err := v.parseToString(c.fd)
	if err != nil {
		return "", err
	}
	return fieldValue, nil
}

// Evaluates and collects the desired fields of each qualified cell associated with a table b-tree leaf page.
func (t *Table) GetCells(fn Condition, cols ...string) ([]*Cell, error) {
	ColOrderMask := make([]bool, len(t.ColOrder))
	foundColsMask := make([]bool, len(cols))
	colsClone := slices.Clone(cols)
	slices.Sort(colsClone)
	greatestIdx := 0
	missing := len(colsClone)
	for i, p := range t.ColOrder {
		if missing == 0 {
			break
		}
		lo, up := 0, len(colsClone)-1
		for lo <= up {
			mid := (lo + up) / 2
			if colsClone[mid] < p {
				lo = mid + 1
			} else if colsClone[mid] > p {
				up = mid - 1
			} else {
				ColOrderMask[i] = true
				foundColsMask[mid] = true
				greatestIdx = i
				missing -= 1
				break
			}
		}
	}

	if missing != 0 {
		unknownCols := []string{}
		for i, found := range foundColsMask {
			if !found {
				unknownCols = append(unknownCols, colsClone[i])
			}
		}
		return nil, fmt.Errorf("unknown elements [%s]", strings.Join(unknownCols, ", "))
	}

	ColOrderMask = ColOrderMask[:greatestIdx+1]
	cellPtrs, err := t.getCellPtrs()
	if err != nil {
		return nil, err
	}

	result := []*Cell{}
	if fn == nil {
		for _, ptr := range cellPtrs {
			cell, err := t.getCellFields(ptr, ColOrderMask)
			if err != nil {
				return nil, err
			}
			result = append(result, cell)
		}
		return result, nil
	}

	for _, ptr := range cellPtrs {
		cell, err := t.getCellFields(ptr, ColOrderMask)
		if err != nil {
			return nil, err
		}
		if ok, err := fn(cell); err != nil {
			return nil, err
		} else if ok {
			result = append(result, cell)
		}
	}
	return result, nil
}

// Collects a sub set of [Table.ColOrder].
//
// The indices of mask map 1:1 with [Table.ColOrder] and can vary in size,
// this means we don't need to iterate over every field just to collect a subset.
func (t *Table) getCellFields(cellPtr int, mask []bool) (*Cell, error) {
	_, cellSizeByteCount, err := decodeVarintAt(t.fd, cellPtr)
	if err != nil {
		return nil, fmt.Errorf("failed to decode recordSize varint: %w", err)
	}

	_, rowIDByteCount, err := decodeVarintAt(t.fd, cellPtr+cellSizeByteCount)
	if err != nil {
		return nil, fmt.Errorf("failed to decode rowid varint: %w", err)
	}

	headerOffset := cellPtr + cellSizeByteCount + rowIDByteCount
	headerSize, headerSizeByteCount, err := decodeVarintAt(t.fd, headerOffset)
	if err != nil {
		return nil, fmt.Errorf("failed to decode record header size varint: %w", err)
	}

	serialTypeOffset := headerOffset + headerSizeByteCount
	fieldValueOffset := headerOffset + int(headerSize)
	fields := map[string]*RecordField{}

	// this is information leakage; parsing the record format the responsibility of dbfile/record.go
	// dbfile/table.go only needs to know about cell field values not how to get them.
	for i, keep := range mask {

		// if you simplified the RecordField interface this could all be abstracted away
		serialType, serialTypeByteCount, err := decodeVarintAt(t.fd, serialTypeOffset) // value is to vague and this interface is ambiguouse
		if err != nil {
			return nil, fmt.Errorf("failed to parse record field: %w", err)
		}

		payloadLen := decodeSerialTypePayloadLen(int(serialType))
		if keep {
			field := &RecordField{
				serialType: int(serialType),
				offset:     fieldValueOffset,
				length:     payloadLen,
			}

			// assuming the first field is always the PK if it is null, then sqlite stores the value in the header
			if i == 0 && serialType == 0 {
				rowIDOffset := cellPtr + cellSizeByteCount
				field.offset = rowIDOffset
			}
			fields[t.ColOrder[i]] = field
		}

		fieldValueOffset += payloadLen
		serialTypeOffset += serialTypeByteCount
	}
	return &Cell{fd: t.fd, fields: fields}, nil
}

func (t *Table) getCellPtrs() ([]int, error) {
	buf := make([]byte, pageCellCountVarint)

	// ASSUMING always b-tree leaf page
	cellPtrArrayOffset := t.pageOffset + cellPtrArrayOffsetFromLeafHeader
	cellPtrArraySizeOffset := t.pageOffset + cellPtrArraySizeOffsetFromHeader

	// possible info leak because DBFile is supposed to
	if t.pageOffset == sqliteSchemaPageOffset {
		cellPtrArraySizeOffset += sqliteSchemaHeaderOffset
		cellPtrArrayOffset += sqliteSchemaHeaderOffset
	}

	if _, err := t.fd.ReadAt(buf, int64(cellPtrArraySizeOffset)); err != nil {
		return nil, fmt.Errorf("failed to read b-tree page header: %w", err)
	}

	cellCount := int(binary.BigEndian.Uint16(buf))

	buf = make([]byte, cellCount*cellPtrVarint)
	_, err := t.fd.ReadAt(buf, int64(cellPtrArrayOffset))
	if err != nil {
		return nil, fmt.Errorf("failed to read b-tree page header: %w", err)
	}

	cellPtrs := make([]int, cellCount)
	for i := range cellCount {
		start := i * cellPtrVarint
		end := (i + 1) * cellPtrVarint
		cellPtrs[i] = int(binary.BigEndian.Uint16(buf[start:end])) + t.pageOffset
	}
	return cellPtrs, nil
}
