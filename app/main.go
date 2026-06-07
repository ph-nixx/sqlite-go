package main

import (
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/codecrafters-io/sqlite-starter-go/dbfile"

	// Available if you need it!
	"github.com/xwb1989/sqlparser"
)

func dotCommand(dbfile *dbfile.DBFile, command string) {
	switch command {
	case ".dbinfo":
		tblNames, err := dbfile.GetTableNames()
		if err != nil {
			log.Fatalln("err", err)
		}

		fmt.Printf("database page size: %v\n", dbfile.PageSize)
		fmt.Printf("number of tables: %v\n", len(tblNames))
	case ".tables":
		names, err := dbfile.GetTableNames()
		if err != nil {
			log.Fatalln(err)
		}

		fmt.Println(strings.Join(names, " "))
	default:
		log.Fatalln("unknown command", command)
	}
}

func isCount(exprs sqlparser.SelectExprs) bool {
	if len(exprs) != 1 {
		return false
	}
	aliased, ok := exprs[0].(*sqlparser.AliasedExpr)
	if !ok {
		return false
	}
	fn, ok := aliased.Expr.(*sqlparser.FuncExpr)
	if !ok {
		return false
	}
	if !fn.IsAggregate() {
		return false
	}
	return fn.Name.EqualString("count")
}

func isSingleTable(tableExprs sqlparser.TableExprs) bool {
	return len(tableExprs) == 1
}

// Usage: your_program.sh sample.db .dbinfo
func main() {
	databaseFilePath := os.Args[1]
	command := os.Args[2]

	fd, err := os.Open(databaseFilePath)
	if err != nil {
		log.Fatalln(err)
	}
	defer fd.Close()

	db, err := dbfile.New(fd)
	if err != nil {
		log.Fatalln(err)
	}

	if len(os.Args) == 3 && strings.HasPrefix(command, ".") {
		dotCommand(db, command)
		return
	}

	stmt, err := sqlparser.Parse(command)
	if err != nil {
		log.Fatalln(err)
	}

	switch stmt := stmt.(type) {
	case *sqlparser.Select:
		if isCount(stmt.SelectExprs) {
			firstTable := stmt.From[0].(*sqlparser.AliasedTableExpr)
			tblName := firstTable.Expr.(sqlparser.TableName).Name.String()
			tbl, err := db.GetTable(tblName)
			if err != nil {
				log.Fatalln(err)
			}

			cells, err := tbl.GetCells(nil, "id")
			if err != nil {
				log.Fatalln(err)
			}

			fmt.Println(len(cells))
		} else if isSingleTable(stmt.From) {
			firstTable := stmt.From[0].(*sqlparser.AliasedTableExpr)
			tblName := firstTable.Expr.(sqlparser.TableName).Name.String()
			tbl, err := db.GetTable(tblName)
			if err != nil {
				log.Fatalln(err)
			}

			cols := []string{}
			for _, expr := range stmt.SelectExprs {
				switch e := expr.(type) {
				case *sqlparser.AliasedExpr:
					if col, ok := e.Expr.(*sqlparser.ColName); ok {
						cols = append(cols, col.Name.String())
					}
				}
			}

			var fn dbfile.Condition = nil
			if stmt.Where != nil {
				comp, _ := stmt.Where.Expr.(*sqlparser.ComparisonExpr)
				col := comp.Left.(*sqlparser.ColName).Name.String()
				val := string(comp.Right.(*sqlparser.SQLVal).Val)

				switch comp.Operator {
				case "=":
					fn = func(cell *dbfile.Cell) (bool, error) {
						fieldValue, err := cell.UnwrapAsString(col)
						if err != nil {
							return false, err
						}
						return fieldValue == val, nil
					}
				}
				cols = append(cols, col)
			}

			cells, err := tbl.GetCells(fn, cols...)
			if err != nil {
				log.Fatalln(err)
			}
			for _, cell := range cells {
				row, err := cell.UnwrapAsStrings(cols...)
				if err != nil {
					log.Fatalln(err)
				}
				fmt.Println(strings.Join(row, "|"))
			}
		}
	}
}
