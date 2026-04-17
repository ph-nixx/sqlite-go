# Code Review: app/main.go

## Issues

### 1. Deferred close before error check (L38-41)
`defer fd.Close()` runs before `err` is checked. If `os.Open` fails, `fd` is nil and `Close()` panics. Swap lines 39 and 40.

### 2. `readUint16Field` does too much and too little
It reads a full buffer just to extract 2 bytes. The abstraction is leaky — callers must know raw byte offsets and buffer sizes. Either read the whole page once and index into it, or make a focused `readUint16At(reader, offset)` that reads exactly 2 bytes at the given file offset.

### 3. Dead code path (L56-57)
`log.Fatalln` already calls `os.Exit(1)`. The `os.Exit(1)` on line 57 is unreachable.

### 4. Magic numbers as constants
`pageSizeOffset = 16`, `cellCountOffset = 3`, etc. are defined as constants but lack any grouping or documentation tying them to the SQLite file format spec. As the parser grows, this will become a wall of unnamed offsets. Consider a struct that models the header layout.

### 5. No separation of concerns
Parsing logic lives directly in `main()`. Extract a `Database` or `SQLiteFile` struct that opens the file and exposes methods like `PageSize()` and `TableCount()`. This will matter immediately when you add `.tables` or query support.

### 6. Redundant `readSize` parameter
Every call to `readUint16Field` passes a buffer size that's tightly coupled to the offset. The function should own that decision, not the caller.

## Recommended next steps

1. Fix the defer/error ordering — it's a runtime bug.
2. Drop the unreachable `os.Exit`.
3. Introduce a `SQLiteFile` struct that reads the header once on open and exposes typed accessors.
4. Delete `readUint16Field`; replace with direct indexing into the cached header bytes.
