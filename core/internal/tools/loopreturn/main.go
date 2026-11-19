// Command loopreturn enforces the house rule that no function returns from
// inside a for or range loop (set the result, break, and return after it).
// It prints each offending return as file:line:column and exits 1 if there
// are any. Returns inside closures in a loop are the closure's own and are
// not counted. Run it through `make lint`.
package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	fileSet := token.NewFileSet()
	var findings []token.Position
	var walkErrs []error
	for _, root := range os.Args[1:] {
		walkErr := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			switch {
			case err != nil:
				return err
			case entry.IsDir() && (entry.Name() == "node_modules" || entry.Name() == "frontend"):
				return filepath.SkipDir
			case entry.IsDir() || !strings.HasSuffix(path, ".go"):
				return nil
			default:
				found, parseErr := loopReturns(fileSet, path)
				findings = append(findings, found...)
				return parseErr
			}
		})
		if walkErr != nil {
			walkErrs = append(walkErrs, walkErr)
		}
	}

	for _, position := range findings {
		fmt.Printf("%s: return inside a loop; set the result, break, and return after it\n", position)
	}
	for _, walkErr := range walkErrs {
		fmt.Fprintln(os.Stderr, "loopreturn:", walkErr)
	}
	if len(findings) > 0 || len(walkErrs) > 0 {
		os.Exit(1)
	}
}

// loopReturns parses one file and returns the position of every return
// statement inside a loop body, closures excluded.
func loopReturns(fileSet *token.FileSet, path string) ([]token.Position, error) {
	file, err := parser.ParseFile(fileSet, path, nil, 0)
	if err != nil {
		return nil, err
	}
	var found []token.Position
	ast.Inspect(file, func(node ast.Node) bool {
		var body *ast.BlockStmt
		switch loop := node.(type) {
		case *ast.ForStmt:
			body = loop.Body
		case *ast.RangeStmt:
			body = loop.Body
		default:
			return true
		}
		ast.Inspect(body, func(inner ast.Node) bool {
			if _, isClosure := inner.(*ast.FuncLit); isClosure {
				return false
			}
			if statement, isReturn := inner.(*ast.ReturnStmt); isReturn {
				found = append(found, fileSet.Position(statement.Pos()))
			}
			return true
		})
		return false
	})
	return found, nil
}
