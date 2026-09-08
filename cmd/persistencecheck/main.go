// Command persistencecheck 检查 GORM 数据访问边界及批准的 pgx 底层例外。
package main

import (
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// 每个例外只开放拥有底层能力的目录或文件，不开放业务 Repository 目录。
var pgxAllowlist = map[string]string{
	"internal/platform/postgres/":                                       "唯一物理连接池、GORM stdlib 和驱动错误投影",
	"internal/platform/migration/":                                      "Atlas 与 River Schema 迁移",
	"internal/platform/gitoperation/":                                   "专用连接的 advisory lock 生命周期",
	"internal/platform/testdb/":                                         "隔离测试库创建、迁移和清理",
	"internal/graph/testfixture/":                                       "显式集成/容量 fixture 的数据准备",
	"internal/workflow/adapter/river/client.go":                         "官方 River pgx Worker 和 listener",
	"internal/workflow/adapter/river/migrator.go":                       "官方 River Schema 迁移",
	"internal/workflow/adapter/river/queue_metrics.go":                  "River 队列运行状态查询",
	"internal/retrieval/adapter/postgres/native_capabilities.go":        "受限原生能力和平台连接访问边界",
	"internal/retrieval/adapter/postgres/native_manifest.go":            "Manifest COPY 与临时表批处理",
	"internal/retrieval/adapter/postgres/native_snapshot.go":            "Snapshot 一致性读取和大批量写入",
	"internal/retrieval/adapter/postgres/native_source_refresh_lock.go": "Source Refresh 专用会话锁",
	"cmd/local-model-runtime-credential-init/main.go":                   "管理角色凭据初始化",
}

// persistenceOwners 覆盖 TODO 10 的 28 个模块、Approval Dispatch 和 Root Grant。
var persistenceOwners = []string{
	"internal/agent/adapter/postgres/", "internal/artifact/adapter/postgres/",
	"internal/audit/adapter/postgres/", "internal/auth/adapter/postgres/",
	"internal/authoring/adapter/postgres/", "internal/capture/adapter/postgres/",
	"internal/changecontrol/adapter/postgres/", "internal/changecontrol/adapter/approvaldispatchpostgres/",
	"internal/collection/adapter/postgres/", "internal/conversation/adapter/postgres/",
	"internal/documenthistory/adapter/postgres/", "internal/events/adapter/postgres/",
	"internal/export/adapter/postgres/", "internal/gitsync/adapter/postgres/",
	"internal/graph/adapter/postgres/", "internal/health/adapter/postgres/",
	"internal/ingestion/adapter/postgres/", "internal/knowledge/adapter/postgres/",
	"internal/localmodelruntime/", "internal/memory/adapter/postgres/",
	"internal/modelsettings/adapter/postgres/", "internal/organizing/adapter/postgres/",
	"internal/retrieval/adapter/postgres/", "internal/review/adapter/postgres/",
	"internal/review/interview/adapter/postgres/", "internal/review/learningpath/adapter/postgres/",
	"internal/tools/adapter/postgres/", "internal/workflow/adapter/postgres/",
	"internal/workspace/adapter/postgres/", "internal/platform/rootgrant/",
}

func main() {
	root := flag.String("root", ".", "仓库根目录")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "persistencecheck: 不接受位置参数")
		os.Exit(2)
	}
	if err := check(*root); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func check(root string) error {
	positions := token.NewFileSet()
	owners := make(map[string]bool, len(persistenceOwners))
	var violations []string
	files := 0
	for _, directory := range []string{"cmd", "internal"} {
		err := filepath.WalkDir(filepath.Join(root, directory), func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() || !strings.HasSuffix(path, ".go") {
				return nil
			}
			relative, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			relative = filepath.ToSlash(relative)
			file, err := parser.ParseFile(positions, path, nil, 0)
			if err != nil {
				return fmt.Errorf("解析 %s: %w", relative, err)
			}
			files++
			production := !strings.HasSuffix(relative, "_test.go")
			packages := make(map[string]bool)
			for _, imported := range file.Imports {
				name, err := strconv.Unquote(imported.Path.Value)
				if err != nil {
					return err
				}
				alias := filepath.Base(name)
				if imported.Name != nil {
					alias = imported.Name.Name
				}
				packages[alias] = true
				pgx := name == "github.com/jackc/pgx/v5" || strings.HasPrefix(name, "github.com/jackc/pgx/v5/")
				gorm := name == "gorm.io/gorm" || strings.HasPrefix(name, "gorm.io/gorm/") || strings.HasPrefix(name, "gorm.io/driver/")
				if production && pgx && !allowedPGX(relative) {
					violations = append(violations, fmt.Sprintf("%s:%d: pgx 超出批准的底层边界", relative, positions.Position(imported.Pos()).Line))
				}
				if production && (pgx || gorm || name == "database/sql") &&
					(strings.Contains(relative, "/application/") || strings.Contains(relative, "/domain/")) {
					violations = append(violations, fmt.Sprintf("%s:%d: 领域/应用层暴露数据库依赖 %s", relative, positions.Position(imported.Pos()).Line, name))
				}
				if production && gorm {
					for _, owner := range persistenceOwners {
						if strings.HasPrefix(relative, owner) {
							owners[owner] = true
						}
					}
				}
			}
			// 测试也不能通过 Model 修改 Schema；只检查真实 AST 调用/引用，忽略文档字符串。
			ast.Inspect(file, func(node ast.Node) bool {
				selector, ok := node.(*ast.SelectorExpr)
				if ok && (selector.Sel.Name == "AutoMigrate" || selector.Sel.Name == "Migrator") && !packageSelector(selector, packages) {
					violations = append(violations, fmt.Sprintf("%s:%d: 禁止通过 GORM %s 修改 Schema", relative, positions.Position(selector.Pos()).Line, selector.Sel.Name))
				}
				if production {
					var name string
					var signature *ast.FuncType
					switch declaration := node.(type) {
					case *ast.FuncDecl:
						name, signature = declaration.Name.Name, declaration.Type
					case *ast.Field:
						if method, isMethod := declaration.Type.(*ast.FuncType); isMethod && len(declaration.Names) == 1 {
							name, signature = declaration.Names[0].Name, method
						}
					}
					if signature != nil && hasOpaqueTransaction(name, signature) {
						violations = append(violations, fmt.Sprintf("%s:%d: %s 使用 opaque any 事务，必须使用 Foundation TransactionScope", relative, positions.Position(node.Pos()).Line, name))
					}
				}
				return true
			})
			return nil
		})
		if err != nil {
			return err
		}
	}
	for _, owner := range persistenceOwners {
		if !owners[owner] {
			violations = append(violations, owner+": 缺少 GORM 持久化实现")
		}
	}
	if len(violations) != 0 {
		return fmt.Errorf("数据访问门禁失败（%d 项）：\n%s", len(violations), strings.Join(violations, "\n"))
	}
	fmt.Printf("数据访问门禁通过：%d 个 owner，%d 个 Go 文件；pgx allowlist、领域边界及 Schema 禁止项均符合约束。\n", len(owners), files)
	return nil
}

// packageSelector 排除 rivermigrate.Migrator 等包类型引用；局部同名变量仍受约束。
func packageSelector(selector *ast.SelectorExpr, packages map[string]bool) bool {
	identifier, ok := selector.X.(*ast.Ident)
	return ok && identifier.Obj == nil && packages[identifier.Name]
}

func hasOpaqueTransaction(name string, signature *ast.FuncType) bool {
	if signature.Params != nil {
		for _, parameter := range signature.Params.List {
			if !anyType(parameter.Type) {
				continue
			}
			for _, identifier := range parameter.Names {
				if identifier.Name == "transaction" || identifier.Name == "tx" {
					return true
				}
			}
			if strings.HasSuffix(name, "Tx") || strings.Contains(name, "Transaction") ||
				strings.HasPrefix(name, "OnWorkflow") || strings.HasPrefix(name, "SafeToCancel") {
				return true
			}
		}
	}
	if strings.Contains(name, "Transaction") && signature.Results != nil {
		for _, result := range signature.Results.List {
			if anyType(result.Type) {
				return true
			}
		}
	}
	return false
}

func anyType(expression ast.Expr) bool {
	if identifier, ok := expression.(*ast.Ident); ok {
		return identifier.Name == "any"
	}
	if empty, ok := expression.(*ast.InterfaceType); ok {
		return empty.Methods == nil || len(empty.Methods.List) == 0
	}
	return false
}

func allowedPGX(path string) bool {
	for allowed := range pgxAllowlist {
		if path == allowed || strings.HasSuffix(allowed, "/") && strings.HasPrefix(path, allowed) {
			return true
		}
	}
	return false
}
