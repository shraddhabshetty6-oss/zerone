package main_test

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLayeringRules(t *testing.T) {
	root := "../../internal"
	if _, err := os.Stat(root); err != nil {
		root = "internal" // If run from repo root
	}

	layerRules := map[string]int{
		"constants":   0,
		"buildinfo":   0,
		"crc":         0,
		"record":      0,
		"faultinject": 0,
		"concurrency": 0,
		"index":       0,
		"codec":       1,
		"storage":     2,
		"wal":         2,
		"tx":          3,
		"retrieval":   3,
		"verify":      3,
		"recovery":    4,
		"engine":      4,
		"httpapi":     5,
	}

	fset := token.NewFileSet()

	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		rel, _ := filepath.Rel(root, path)
		pkgName := filepath.Dir(rel)
		pkgName = filepath.ToSlash(pkgName)
		if idx := strings.Index(pkgName, "/"); idx >= 0 {
			pkgName = pkgName[:idx]
		}

		pkgLayer, ok := layerRules[pkgName]
		if !ok {
			return nil
		}

		node, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}

		for _, imp := range node.Imports {
			importPath := strings.Trim(imp.Path.Value, `"`)
			if !strings.HasPrefix(importPath, "studentdb/internal/") {
				continue // Standard library
			}

			targetPkg := strings.TrimPrefix(importPath, "studentdb/internal/")
			targetLayer, targetKnown := layerRules[targetPkg]
			if !targetKnown {
				t.Errorf("Package %s imports unknown internal package %s in %s", pkgName, targetPkg, path)
				continue
			}

			// Invariant: dependencies point downward only; no upward imports
			if targetLayer >= pkgLayer && pkgLayer > 0 {
				// Special allowed exception: L4 recovery can import L3 verify; verify must never import recovery
				if pkgName == "verify" && targetPkg == "recovery" {
					t.Errorf("Layer violation: verify (L3) imports recovery (L4) in %s", path)
				}
				if targetLayer > pkgLayer {
					t.Errorf("Layer violation: %s (L%d) imports %s (L%d) in %s", pkgName, pkgLayer, targetPkg, targetLayer, path)
				}
			} else if pkgLayer == 0 && targetLayer > 0 {
				t.Errorf("Layer violation: L0 leaf package %s imports %s (L%d) in %s", pkgName, targetPkg, targetLayer, path)
			}
		}

		return nil
	})

	if err != nil {
		t.Fatalf("Walk error: %v", err)
	}
}

