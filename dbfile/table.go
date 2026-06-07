package dbfile

import (
	"encoding/binary"
	"fmt"
	"io"
	"slices"
	"strings"
)

const (
	interiorPageType               = 0x05
	leafPageType                   = 0x0d
	pageTypeLen                    = 1
	pageCellPtrArrayCellCount      = 3
	leafPageCellPtrArray           = 8
	interiorPageCellPtrArray       = 12
	interiorPageRightMostPtr       = 8
	cellPtrLen                     = 2
	pageCellCountLen               = 2
	interiorCellChildPageNumberLen = 4
	sqliteSchemaHeaderPtr          = 100
)

type Table struct {
	fd       io.ReaderAt
	ColOrder []string
	rootpage int
	pageSize int
}

// The on disk representation of a sqlite row: a set of fields with the meta data to collection them.
// Table leaf cell.
type Cell struct {
	fd     io.ReaderAt
	fields map[string]*RecordField
}

type Condition func(*Cell) (bool, error)

func (c *Cell) UnwrapAsStrings(colOrder ...string) ([]string, error) {
	// if len(colOrder) != len(c.fields) {
	// 	return nil, fmt.Errorf(
	// 		"'%d' columns provided expected '%d'",
	// 		len(colOrder),
	// 		len(c.fields),
	// 	)
	// }
	//

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
	fieldValue, err := v.parseToString(c.fd)
	if err != nil {
		return "", err
	}
	return fieldValue, nil
}

// Evaluates and returns only the desired fields of each qualified cell associated with a table leaf page.
//
// Refactor will unable collection from all table leaf pages of a table b-tree by recursively traversing from the root page.
func (t *Table) GetCells(fn Condition, cols ...string) ([]*Cell, error) {
	if fn == nil {
		fn = func(c *Cell) (bool, error) { return true, nil }
	}

	seen := map[string]bool{}
	uniqueCols := make([]string, 0, len(cols))
	for _, col := range cols {
		if !seen[col] {
			uniqueCols = append(uniqueCols, col)
			seen[col] = true
		}
	}
	slices.Sort(uniqueCols)
	colOrderMask := make([]bool, len(t.ColOrder))
	foundColsMask := make([]bool, len(cols))
	greatestIdx := 0
	missing := len(uniqueCols)
	for i, p := range t.ColOrder {
		if missing == 0 {
			break
		}
		lo, up := 0, len(uniqueCols)-1
		for lo <= up {
			mid := (lo + up) / 2
			if uniqueCols[mid] < p {
				lo = mid + 1
			} else if uniqueCols[mid] > p {
				up = mid - 1
			} else {
				colOrderMask[i] = true
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
				unknownCols = append(unknownCols, uniqueCols[i])
			}
		}
		return nil, fmt.Errorf("undefined cols [%s]", strings.Join(unknownCols, ", "))
	}

	leafCells, err := t.getLeafCells(t.rootpage, colOrderMask[:greatestIdx+1], fn)
	if err != nil {
		return nil, err
	}
	return leafCells, nil
}

func (t *Table) getLeafCells(pageNumber int, mask []bool, fn Condition) ([]*Cell, error) {
	pagePtr := (pageNumber - 1) * t.pageSize
	buf := make([]byte, pageTypeLen)
	if _, err := t.fd.ReadAt(buf, int64(pagePtr)); err != nil {
		return nil, err
	}

	pageType := buf[0]
	var cellPtrArrayPtr int
	var cellPtrArrayCellCountPtr int
	if pageNumber == sqliteSchemaPageNumber {
		cellPtrArrayPtr = sqliteSchemaHeaderPtr + leafPageCellPtrArray
		cellPtrArrayCellCountPtr = sqliteSchemaHeaderPtr + pageCellPtrArrayCellCount
	} else if pageType == interiorPageType {
		cellPtrArrayPtr = pagePtr + interiorPageCellPtrArray
		cellPtrArrayCellCountPtr = pagePtr + pageCellPtrArrayCellCount
	} else if pageType == leafPageType {
		cellPtrArrayPtr = pagePtr + leafPageCellPtrArray
		cellPtrArrayCellCountPtr = pagePtr + pageCellPtrArrayCellCount
	} else {
		return nil, fmt.Errorf("undefined page type [%v]", buf[0])
	}

	buf = make([]byte, pageCellCountLen)
	if _, err := t.fd.ReadAt(buf, int64(cellPtrArrayCellCountPtr)); err != nil {
		return nil, fmt.Errorf("failed to read table page header [%w]", err)
	}

	cellCount := int(binary.BigEndian.Uint16(buf))
	buf = make([]byte, cellCount*cellPtrLen)
	_, err := t.fd.ReadAt(buf, int64(cellPtrArrayPtr))
	if err != nil {
		return nil, fmt.Errorf("failed to read table page header: [%w]", err)
	}

	cellPtrs := make([]int, cellCount)
	for i := range cellCount {
		start := i * cellPtrLen
		end := (i + 1) * cellPtrLen
		cellPtrs[i] = int(binary.BigEndian.Uint16(buf[start:end])) + pagePtr
	}

	result := []*Cell{}
	if pageType == interiorPageType {
		for _, ptr := range cellPtrs {
			buf := make([]byte, interiorCellChildPageNumberLen)
			if _, err := t.fd.ReadAt(buf, int64(ptr)); err != nil {
				return nil, err
			}

			childPageNumber := int(binary.BigEndian.Uint32(buf))
			fmt.Println(childPageNumber, len(result))
			cells, err := t.getLeafCells(childPageNumber, mask, fn)
			if err != nil {
				return nil, err
			}
			result = append(result, cells...)
		}

		buf = make([]byte, interiorCellChildPageNumberLen)
		rightMostPtr := pagePtr + interiorPageRightMostPtr
		if _, err := t.fd.ReadAt(buf, int64(rightMostPtr)); err != nil {
			return nil, err
		}

		childPageNumber := int(binary.BigEndian.Uint32(buf))
		cells, err := t.getLeafCells(childPageNumber, mask, fn)
		if err != nil {
			return nil, err
		}
		result = append(result, cells...)
		return result, nil
	}

	for _, ptr := range cellPtrs {
		cell, err := t.getLeafCellFields(ptr, mask)
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

// Collects a table leaf page cell with a sub set fields choosen from [Table.ColOrder].
//
// The indices of mask map 1:1 with [Table.ColOrder] and can vary in size,
// this means we don't need to iterate over every field just to collect a subset.
func (t *Table) getLeafCellFields(cellPtr int, mask []bool) (*Cell, error) {
	_, cellSizeByteCount, err := decodeVarintAt(t.fd, cellPtr)
	if err != nil {
		return nil, fmt.Errorf("failed to decode recordSize varint: %w", err)
	}

	_, rowIDByteCount, err := decodeVarintAt(t.fd, cellPtr+cellSizeByteCount)
	if err != nil {
		return nil, fmt.Errorf("failed to decode rowid varint: %w", err)
	}

	headerPtr := cellPtr + cellSizeByteCount + rowIDByteCount
	headerSize, headerSizeByteCount, err := decodeVarintAt(t.fd, headerPtr)
	if err != nil {
		return nil, fmt.Errorf("failed to decode record header size varint: %w", err)
	}

	serialTypePtr := headerPtr + headerSizeByteCount
	fieldValuePtr := headerPtr + int(headerSize)
	fields := map[string]*RecordField{}

	// this is information leakage; parsing the record format the responsibility of dbfile/record.go
	// dbfile/table.go only needs to know about cell field values not how to get them.
	for i, keep := range mask {

		// if you simplified the RecordField interface this could all be abstracted away
		serialType, serialTypeByteCount, err := decodeVarintAt(t.fd, serialTypePtr) // value is to vague and this interface is ambiguouse
		if err != nil {
			return nil, fmt.Errorf("failed to parse record field: %w", err)
		}

		payloadLen := decodeSerialTypePayloadLen(int(serialType))
		if keep {
			field := &RecordField{
				serialType: int(serialType),
				ptr:        fieldValuePtr,
				length:     payloadLen,
			}

			// assuming the first field is always the PK if it is null, then sqlite stores the value in the header
			if i == 0 && serialType == 0 {
				rowIDPtr := cellPtr + cellSizeByteCount
				field.ptr = rowIDPtr
			}
			fields[t.ColOrder[i]] = field
		}

		fieldValuePtr += payloadLen
		serialTypePtr += serialTypeByteCount
	}
	return &Cell{fd: t.fd, fields: fields}, nil
}
