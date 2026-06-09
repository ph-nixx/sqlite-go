package dbfile

import (
	"encoding/binary"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
)

const (
	interiorPageType             = 0x05
	leafPageType                 = 0x0d
	indexInteriorPageType        = 0x02
	indexLeafPageType            = 0x0a
	cellPtrAryCellCntStrt        = 3
	cellPtrAryCellCntEnd         = cellPtrAryCellCntStrt + 2
	leafPageCellPtrAryStrt       = 8
	interiorPageCellPtrAryStrt   = 12
	interiorPageRightMostPtrStrt = 8
	interiorPageRightMostPtrEnd  = 12
	cellPtrLen                   = 2
	pageNumLen                   = 4
	sqliteSchemaHeaderOfs        = 100
	pageTypeOfs                  = 0
	rowIdLen                     = 2
	indexRowIdField              = "rowid"
)

// indices only support single column indices, hence the 'string' key type for the map.
type Table struct {
	fd       io.ReaderAt
	ColOrder []string
	rootpage int
	pageSize int
	indices  map[string]*index
}

type index struct {
	pageNum  int
	colOrder []string
}

type Condition struct {
	Cols []string
	Eq   func(*Cell) (bool, error)
	Gt   func(*Cell) (bool, error)
}

type split struct {
	rowIds  []int
	pageNum int
}

// Evaluates and returns only the desired fields of each qualified cell associated with a table leaf page.
//
// Refactor will unable collection from all table leaf pages of a table b-tree by recursively traversing from the root page.
func (t *Table) GetLeafCells(con *Condition, cols ...string) ([]*Cell, error) {
	if con != nil {
		cols = append(cols, con.Cols...)
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

	var rowIds []int
	if con != nil && len(t.indices) > 0 {
		for _, c := range con.Cols {
			index, ok := t.indices[c]
			if ok {
				r, err := t.checkIndex(index.pageNum, index.colOrder, con)
				if err != nil {
					return nil, err
				}
				rowIds = r
				break
			}
		}
	}
	if con == nil {
		con = &Condition{Cols: nil, Eq: func(c *Cell) (bool, error) { return true, nil }}
	}

	leafCells, err := t.getLeafCells(t.rootpage, colOrderMask[:greatestIdx+1], con, rowIds)
	if err != nil {
		return nil, err
	}
	return leafCells, nil
}

// Uses a set of unique cols from a WHERE clause to traverse a index b-tree and collect the rowIds at each cell.
func (t *Table) checkIndex(
	pageNum int,
	colOrder []string,
	con *Condition,
) ([]int, error) {
	pagePtr := (pageNum - 1) * t.pageSize
	page := make([]byte, t.pageSize)
	if _, err := t.fd.ReadAt(page, int64(pagePtr)); err != nil {
		return nil, err
	}

	pageType := page[pageTypeOfs]
	var cellPtrBuf []byte
	var cellCount int
	switch pageType {
	case indexInteriorPageType:
		buf := page[cellPtrAryCellCntStrt:cellPtrAryCellCntEnd]
		cellCount = int(binary.BigEndian.Uint16(buf))
		cellPtrBuf = page[interiorPageCellPtrAryStrt : interiorPageCellPtrAryStrt+cellCount*cellPtrLen]
	case indexLeafPageType:
		buf := page[cellPtrAryCellCntStrt:cellPtrAryCellCntEnd]
		cellCount = int(binary.BigEndian.Uint16(buf))
		cellPtrBuf = page[leafPageCellPtrAryStrt : leafPageCellPtrAryStrt+cellCount*cellPtrLen]
	default:
		return nil, fmt.Errorf("page type '%v' is undefined", pageType)
	}

	var result []int
	if pageType == indexInteriorPageType {
		skipRightMostChild := false
		for i := range cellCount {
			start := i * cellPtrLen
			end := (i + 1) * cellPtrLen
			cellPtr := int(binary.BigEndian.Uint16(cellPtrBuf[start:end]))
			cell, err := getCellPayload(page[cellPtr+pageNumLen:], colOrder, nil)
			if err != nil {
				return nil, err
			}

			gt, err := con.Gt(cell)
			if err != nil {
				return nil, err
			}
			eq, err := con.Eq(cell)
			if err != nil {
				return nil, err
			}

			if eq {
				rowId, err := cell.UnwrapAsString(indexRowIdField)
				if err != nil {
					return nil, err
				}
				num, err := strconv.Atoi(rowId)
				if err != nil {
					return nil, err
				}
				result = append(result, int(num))
			}
			if gt || eq {
				leftChildPageNum := int(binary.BigEndian.Uint32(page[cellPtr : cellPtr+pageNumLen]))
				rowIds, err := t.checkIndex(leftChildPageNum, colOrder, con)
				if err != nil {
					return nil, err
				}
				result = append(result, rowIds...)
				skipRightMostChild = true
				break
			}
		}
		if !skipRightMostChild {
			childPagebuf := page[interiorPageRightMostPtrStrt:interiorPageRightMostPtrEnd]
			childPageNum := int(binary.BigEndian.Uint32(childPagebuf))
			rowIds, err := t.checkIndex(childPageNum, colOrder, con)
			if err != nil {
				return nil, err
			}
			result = append(result, rowIds...)
		}
		return result, nil
	}

	// index leaf cell
	for i := range cellCount {
		start := i * cellPtrLen
		end := (i + 1) * cellPtrLen
		cellPtr := int(binary.BigEndian.Uint16(cellPtrBuf[start:end]))
		cell, err := getCellPayload(page[cellPtr:], colOrder, nil)
		if err != nil {
			return nil, err
		}
		if ok, err := con.Eq(cell); err != nil {
			return nil, err
		} else if ok {
			rowId, err := cell.UnwrapAsString(indexRowIdField)
			if err != nil {
				return nil, err
			}
			num, err := strconv.Atoi(rowId)
			if err != nil {
				return nil, err
			}
			result = append(result, int(num))
		}
	}
	return result, nil
}

func (t *Table) getLeafCells(pageNum int, mask []bool, con *Condition, rowIds []int) ([]*Cell, error) {
	pagePtr := (pageNum - 1) * t.pageSize
	page := make([]byte, t.pageSize)
	if _, err := t.fd.ReadAt(page, int64(pagePtr)); err != nil {
		return nil, err
	}

	pageType := page[pageTypeOfs]
	var cellPtrBuf []byte
	var cellCount int
	if pageType == interiorPageType {
		buf := page[cellPtrAryCellCntStrt:cellPtrAryCellCntEnd]
		cellCount = int(binary.BigEndian.Uint16(buf))
		cellPtrBuf = page[interiorPageCellPtrAryStrt : interiorPageCellPtrAryStrt+cellCount*cellPtrLen]
	} else if pageType == leafPageType {
		buf := page[cellPtrAryCellCntStrt:cellPtrAryCellCntEnd]
		cellCount = int(binary.BigEndian.Uint16(buf))
		cellPtrBuf = page[leafPageCellPtrAryStrt : leafPageCellPtrAryStrt+cellCount*cellPtrLen]
	} else if pageNum == sqliteSchemaPageNum {
		strt := sqliteSchemaHeaderOfs + cellPtrAryCellCntStrt
		end := sqliteSchemaHeaderOfs + cellPtrAryCellCntEnd
		cellCount = int(binary.BigEndian.Uint16(page[strt:end]))
		strt = sqliteSchemaHeaderOfs + leafPageCellPtrAryStrt
		end = sqliteSchemaHeaderOfs + leafPageCellPtrAryStrt + cellCount*cellPtrLen
		cellPtrBuf = page[strt:end]
	} else {
		return nil, fmt.Errorf("page type '%v' is undefined", pageType)
	}

	result := []*Cell{}
	if pageType == interiorPageType && rowIds == nil {
		for i := range cellCount {
			start := i * cellPtrLen
			end := (i + 1) * cellPtrLen
			cellPtr := int(binary.BigEndian.Uint16(cellPtrBuf[start:end]))
			rowIdStrt := cellPtr + pageNumLen
			buf := page[cellPtr:rowIdStrt]
			pageNum := int(
				binary.BigEndian.Uint32(buf),
			)
			cells, err := t.getLeafCells(pageNum, mask, con, rowIds)
			if err != nil {
				return nil, err
			}
			result = append(result, cells...)
		}

		buf := page[interiorPageRightMostPtrStrt:interiorPageRightMostPtrEnd]
		pageNum := int(binary.BigEndian.Uint32(buf))
		cells, err := t.getLeafCells(pageNum, mask, con, rowIds)
		if err != nil {
			return nil, err
		}
		result = append(result, cells...)
		return result, nil
	}

	// assuming this mechanism will only traverse to a sequence of leaf cells that contains the desired field value
	if pageType == interiorPageType && rowIds != nil {
		buf := page[interiorPageRightMostPtrStrt:interiorPageRightMostPtrEnd]
		pageNum := int(binary.BigEndian.Uint32(buf))
		splits, err := getRowIdSplits(rowIds, cellPtrBuf, pageNum, page)
		if err != nil {
			return nil, err
		}
		for _, s := range splits {
			cells, err := t.getLeafCells(s.pageNum, mask, con, s.rowIds)
			if err != nil {
				return nil, err
			}
			result = append(result, cells...)
		}
		return result, nil
	}

	for i := range cellCount {
		start := i * cellPtrLen
		end := (i + 1) * cellPtrLen
		cellPtr := int(binary.BigEndian.Uint16(cellPtrBuf[start:end]))
		cell, err := getCellPayload(page[cellPtr:], t.ColOrder, mask)
		if err != nil {
			return nil, err
		}
		if ok, err := con.Eq(cell); err != nil {
			return nil, err
		} else if ok {
			result = append(result, cell)
		}
	}
	return result, nil
}

// Assuming [rowIds] are sorted in accending order this function will group each sub sequences of row IDs
// with the page number that has a key which is greater than or equal to the last row ID in the sequence.
func getRowIdSplits(rowIds []int, cellPtrBuf []byte, rightMostPageNum int, page []byte) ([]split, error) {
	if len(cellPtrBuf) == 0 || len(cellPtrBuf)%cellPtrLen != 0 {
		return nil, fmt.Errorf("a cell pointer array must be a multiple of '%d' bytes", cellPtrLen)
	}

	cellCount := len(cellPtrBuf) / cellPtrLen
	result := make([]split, 0, cellCount+1)
	splitInx := 0
	for i := 0; i < cellCount && splitInx < len(rowIds); i += 1 {
		cellPtr := int(binary.BigEndian.Uint16(cellPtrBuf[cellPtrLen*i : cellPtrLen*(i+1)]))
		pageNum := int(binary.BigEndian.Uint16(page[cellPtr : cellPtr+pageNumLen]))
		key, _ := decodeVarint(page[cellPtr+pageNumLen:])

		j := splitInx
		for j < len(rowIds) && rowIds[j] <= key {
			j += 1
		}
		if j > splitInx {
			result = append(result, split{rowIds[splitInx:j], pageNum})
		}
	}
	if splitInx < len(rowIds) {
		result = append(result, split{rowIds[splitInx:], rightMostPageNum})
	}

	return result, nil
}

// Collects a table leaf page cell with a sub set fields choosen from [Table.ColOrder].
//
// The indices of mask map 1:1 with [Table.ColOrder] and can vary in size,
// this means we don't need to iterate over every field just to collect a subset.
func getCellPayload(cellBuf []byte, colOrder []string, mask []bool) (*Cell, error) {
	_, cellSizeLen := decodeVarint(cellBuf)
	_, rowIdLen := decodeVarint(cellBuf[cellSizeLen:])
	headerOfs := cellSizeLen + rowIdLen
	headerSize, headerSizeByteCount := decodeVarint(cellBuf[headerOfs:])

	serialTypeOfs := headerOfs + headerSizeByteCount
	fieldValueOfs := headerOfs + headerSize
	fields := map[string]*RecordField{}

	// this is information leakage; parsing the record format the responsibility of dbfile/record.go
	// dbfile/table.go only needs to know about cell field values not how to get them.
	for i := 0; (mask != nil && i < len(mask)) || (mask == nil && i < len(colOrder)); i++ {

		// if you simplified the RecordField interface this could all be abstracted away
		serialType, serialTypeByteCount := decodeVarint(cellBuf[serialTypeOfs:]) // value is to vague and this interface is ambiguouse
		payloadLen := decodeSerialTypePayloadLen(int(serialType))
		if (mask != nil && mask[i]) || mask == nil {
			field := &RecordField{
				serialType: int(serialType),
				buf:        cellBuf[fieldValueOfs : fieldValueOfs+payloadLen],
			}

			// assuming the first field is always the PK if it is null, then sqlite stores the value in the header
			if i == 0 && serialType == 0 {
				field.buf = cellBuf[cellSizeLen : cellSizeLen+rowIdLen]
			}
			fields[colOrder[i]] = field
		}

		fieldValueOfs += payloadLen
		serialTypeOfs += serialTypeByteCount
	}
	return &Cell{fields: fields}, nil
}
