package main

import (
	"fmt"
	"github.com/codecrafters-io/sqlite-starter-go/dbfile"
	"log"
	"os"
	"strings"

	// Available if you need it!
	"github.com/xwb1989/sqlparser"
)

func dotCommand(dbfile *dbfile.DBFile, command string) {
	switch command {
	case ".dbinfo":
		_, cellCount, err := dbfile.SqliteSchema.GetCellPtrArray()
		if err != nil {
			log.Fatalln(err)
		}

		fmt.Printf("database page size: %v\n", dbfile.PageSize)
		fmt.Printf("number of tables: %v\n", cellCount)
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

	dbfile, err := dbfile.New(fd)
	if err != nil {
		log.Fatalln(err)
	}

	if len(os.Args) == 3 && strings.HasPrefix(command, ".") {
		dotCommand(dbfile, command)
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
			tbl, err := dbfile.GetTable(tblName)
			if err != nil {
				log.Fatalln(err)
			}

			_, cellCount, err := tbl.GetCellPtrArray()
			if err != nil {
				log.Fatalln(err)
			}

			fmt.Println(cellCount)
		} else if isSingleTable(stmt.From) {
			firstTable := stmt.From[0].(*sqlparser.AliasedTableExpr)
			tblName := firstTable.Expr.(sqlparser.TableName).Name.String()
			tbl, err := dbfile.GetTable(tblName)
			if err != nil {
				log.Fatalln(err)
			}

			cols := strings.Split(sqlparser.String(stmt.SelectExprs), ", ")
			rows, err := tbl.GetRowFields(cols)
			if err != nil {
				log.Fatalln(err)
			}

			for _, row := range rows {
				fmt.Println(strings.Join(row, "|"))
			}
		}
	}
}
