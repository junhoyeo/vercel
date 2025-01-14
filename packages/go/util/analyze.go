package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var ignoredFoldersRegex []*regexp.Regexp

func init() {
	ignoredFolders := []string{"vendor", "testdata", ".now", ".vercel"}

	// Build the regex that matches if a path contains the respective ignored folder
	// The pattern will look like: (.*/)?vendor/.*, which matches every path that contains a vendor folder
	for _, folder := range ignoredFolders {
		ignoredFoldersRegex = append(ignoredFoldersRegex, regexp.MustCompile("(.*/)?"+folder+"/.*"))
	}
}

type analyze struct {
	PackageName string   `json:"packageName"`
	FuncName    string   `json:"functionName"`
	Watch       []string `json:"watch"`
}

// parse go file
func parse(fileName string) *ast.File {
	fset := token.NewFileSet()
	parsed, err := parser.ParseFile(fset, fileName, nil, parser.ParseComments)
	if err != nil {
		log.Fatalf("Could not parse Go file \"%s\"\n", fileName)
		os.Exit(1)
	}

	return parsed
}

// ensure we only working with interest go file(s)
func visit(files *[]string) filepath.WalkFunc {
	return func(path string, info os.FileInfo, err error) error {
		itf, err := filepath.Match("*test.go", path)
		if err != nil {
			log.Fatal(err)
		}

		// we don't need Dirs, or test files
		// we only want `.go` files. Further, we ignore
		// every file that is in one of the ignored folders.
		if info.IsDir() || itf || filepath.Ext(path) != ".go" || isInIgnoredFolder(path) {
			return nil
		}

		*files = append(*files, path)
		return nil
	}
}

// isInIgnoredFolder checks if the given path is in one of the ignored folders.
func isInIgnoredFolder(path string) bool {
	// Make sure the regex works for Windows paths
	path = filepath.ToSlash(path)

	for _, pattern := range ignoredFoldersRegex {
		if pattern.MatchString(path) {
			return true
		}
	}
	return false
}

// return unique file
func unique(files []string) []string {
	encountered := map[string]bool{}
	for v := range files {
		encountered[files[v]] = true
	}

	result := []string{}
	for key := range encountered {
		result = append(result, key)
	}
	return result
}

func main() {
	if len(os.Args) != 3 {
		// Args should have the program name on `0`
		// and the file name on `1`
		fmt.Println("Wrong number of args; Usage is:\n  ./go-analyze -modpath=module-path file_name.go")
		os.Exit(1)
	}
	fileName := os.Args[2]
	rf, err := ioutil.ReadFile(fileName)
	if err != nil {
		log.Fatal(err)
	}
	se := string(rf)

	var files []string
	var relatedFiles []string

	// Add entrypoint to watchlist
	relFileName, err := filepath.Rel(filepath.Dir(fileName), fileName)
	if err != nil {
		log.Fatal(err)
	}
	relatedFiles = append(relatedFiles, relFileName)

	// looking for all go files that have export func
	// using in entrypoint
	err = filepath.Walk(filepath.Dir(fileName), visit(&files))
	if err != nil {
		log.Fatal(err)
	}

	// looking related packages
	var modPath string
	flag.StringVar(&modPath, "modpath", "", "module path")
	flag.Parse()
	if len(modPath) > 1 {
		err = filepath.Walk(modPath, visit(&files))
		if err != nil {
			log.Fatal(err)
		}
	}

	for _, file := range files {
		absFileName, _ := filepath.Abs(fileName)
		absFile, _ := filepath.Abs(file)
		// if it isn't entrypoint
		if absFileName != absFile {
			// find all export structs and functions
			pf := parse(file)
			var exportedDecl []string

			ast.Inspect(pf, func(n ast.Node) bool {
				switch t := n.(type) {
				case *ast.FuncDecl:
					if t.Name.IsExported() {
						exportedDecl = append(exportedDecl, t.Name.Name)
					}
				// find variable declarations
				case *ast.TypeSpec:
					// which are public
					if t.Name.IsExported() {
						switch t.Type.(type) {
						// and are interfaces
						case *ast.StructType:
							exportedDecl = append(exportedDecl, t.Name.Name)
						}
					}
				}
				return true
			})

			for _, ed := range exportedDecl {
				if strings.Contains(se, ed) {
					// find relative path of related file
					var basePath string
					if modPath == "" {
						basePath = filepath.Dir(fileName)
					} else {
						basePath = modPath
					}

					rel, err := filepath.Rel(basePath, file)
					if err != nil {
						log.Fatal(err)
					}
					relatedFiles = append(relatedFiles, rel)
				}
			}
		}
	}

	parsed := parse(fileName)
	offset := parsed.Pos()

	for _, decl := range parsed.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			// this declaration is not a function
			// so we're not interested
			continue
		}
		if fn.Name.IsExported() == true {
			// find a valid `http.HandlerFunc` handler function
			params := rf[fn.Type.Params.Pos()-offset : fn.Type.Params.End()-offset]
			validHandlerFunc := (strings.Contains(string(params), "http.ResponseWriter") &&
				strings.Contains(string(params), "*http.Request") &&
				len(fn.Type.Params.List) == 2 &&
				(fn.Recv == nil || len(fn.Recv.List) == 0))

			if validHandlerFunc {
				// we found the first exported function with `http.HandlerFunc`
				// we're done!
				analyzed := analyze{
					PackageName: parsed.Name.Name,
					FuncName:    fn.Name.Name,
					Watch:       unique(relatedFiles),
				}
				analyzedJSON, _ := json.Marshal(analyzed)
				fmt.Print(string(analyzedJSON))
				os.Exit(0)
			}
		}
	}

	// fallback, when ast coudn't parse, with multi-line comments
	for _, decl := range parsed.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		if fn.Name.IsExported() == true {
			for _, param := range fn.Type.Params.List {
				paramStr := fmt.Sprintf("%s", param.Type)
				if strings.Contains(string(paramStr), "http ResponseWriter") && len(fn.Type.Params.List) == 2 && (fn.Recv == nil || len(fn.Recv.List) == 0) {
					analyzed := analyze{
						PackageName: parsed.Name.Name,
						FuncName:    fn.Name.Name,
						Watch:       unique(relatedFiles),
					}
					analyzedJSON, _ := json.Marshal(analyzed)
					fmt.Print(string(analyzedJSON))
					os.Exit(0)
				}
			}
		}
	}
}
