package river

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRiverInsertionSurfaceIsTransactionOnly(t *testing.T) {
	t.Parallel()

	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve architecture test source path")
	}
	packageDir := filepath.Dir(currentFile)

	clientFile := parseGoFile(t, filepath.Join(packageDir, "..", "..", "..", "workflow", "adapter", "river", "client.go"))
	for _, declaration := range clientFile.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Recv == nil || receiverType(function.Recv.List[0].Type) != "Client" {
			continue
		}
		if token.IsExported(function.Name.Name) && strings.HasPrefix(function.Name.Name, "Insert") {
			t.Fatalf("workflow River Client exposes non-transactional insertion method %s", function.Name.Name)
		}
	}

	dispatcherFile := parseGoFile(t, filepath.Join(packageDir, "dispatcher.go"))
	constructors := make([]string, 0, 1)
	for _, declaration := range dispatcherFile.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Recv != nil || !returnsDispatcher(function.Type.Results) || !token.IsExported(function.Name.Name) {
			continue
		}
		constructors = append(constructors, function.Name.Name)
	}
	if len(constructors) != 1 || constructors[0] != "NewTransactionalDispatcher" {
		t.Fatalf("export River dispatcher constructors = %v, want only NewTransactionalDispatcher", constructors)
	}
}

func parseGoFile(t *testing.T, path string) *ast.File {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return file
}

func receiverType(expression ast.Expr) string {
	switch value := expression.(type) {
	case *ast.Ident:
		return value.Name
	case *ast.StarExpr:
		return receiverType(value.X)
	default:
		return ""
	}
}

func returnsDispatcher(results *ast.FieldList) bool {
	if results == nil {
		return false
	}
	for _, result := range results.List {
		if receiverType(result.Type) == "Dispatcher" {
			return true
		}
	}
	return false
}
