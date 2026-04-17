package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"fmt"
	"log"
	"os"
	// Available if you need it!
	// "github.com/xwb1989/sqlparser"
)

func getSchemaCellCount(reader *bufio.Reader) (uint16, error) {
	buf := make([]byte, 8)
	n, err := reader.Read(buf)
	if err != nil {
		return 0, err
	}
	if n != 8 {
		return 0, fmt.Errorf("result: b-tree schema_header is %d bytes, expected: always 8 bytes", n)
	}
	var cellCount uint16
	if err := binary.Read(bytes.NewReader(buf[3:5]), binary.BigEndian, &cellCount); err != nil {
		return 0, fmt.Errorf("failed to read integer: %w", err)
	}
	return cellCount, nil
}

// Usage: your_program.sh sample.db .dbinfo
func main() {
	databaseFilePath := os.Args[1]
	command := os.Args[2]

	switch command {
	case ".dbinfo":
		databaseFile, err := os.Open(databaseFilePath)
		if err != nil {
			log.Fatal(err)
		}
		reader := bufio.NewReader(databaseFile)
		header := make([]byte, 100)
		n, err := reader.Read(header)
		if err != nil {
			log.Fatalln("failed to read sqlite file header:", err)
		}
		if n != 100 {
			log.Printf("result: sqlite file header is %d bytes, expected: always 100 bytes", n)
		}

		var pageSize uint16
		if err := binary.Read(bytes.NewReader(header[16:18]), binary.BigEndian, &pageSize); err != nil {
			fmt.Println("Failed to read integer:", err)
			return
		}
		fmt.Fprintln(os.Stderr, "Logs from your program will appear here!")
		cellCount, err := getSchemaCellCount(reader)
		fmt.Printf("database page size: %v\n", pageSize)
		fmt.Printf("number of tables: %v", cellCount)
	default:
		fmt.Println("Unknown command", command)
		os.Exit(1)
	}
}
