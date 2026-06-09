package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/get-vix/vix/internal/daemon/brain"
	"github.com/get-vix/vix/internal/daemon/brain/lsp"
)

// lspQueryImpl implements the lsp_query tool.
func lspQueryImpl(operation, file, query string, line, character int, includeDecl bool, cwd string) (string, error) {
	pool := lsp.GetPool()
	if pool == nil {
		return "", fmt.Errorf("LSP not available: no LSP pool initialized, ensure .vix/settings.json has LSP servers configured and brain.init has been run")
	}

	switch operation {
	case "workspace_symbols":
		return lspWorkspaceSymbols(pool, query)
	case "go_to_definition", "find_references", "hover", "document_symbols", "find_implementations", "diagnostics":
		return lspFileOperation(pool, operation, file, line, character, includeDecl, cwd)
	default:
		return "", fmt.Errorf("unknown LSP operation: %s", operation)
	}
}

func lspFileOperation(pool *lsp.Pool, operation, file string, line, character int, includeDecl bool, cwd string) (string, error) {
	if file == "" {
		return "", fmt.Errorf("file parameter is required for %s operation", operation)
	}

	// Resolve file path
	absFile := file
	if !filepath.IsAbs(file) {
		absFile = filepath.Join(cwd, file)
	}

	if _, err := os.Stat(absFile); os.IsNotExist(err) {
		return "", fmt.Errorf("file not found: %s", file)
	}

	ext := strings.ToLower(filepath.Ext(absFile))
	language := brain.LanguageForExt(ext)
	if language == "" {
		return "", fmt.Errorf("no language mapping for extension %s", ext)
	}

	client, err := pool.GetClient(language)
	if err != nil {
		return "", fmt.Errorf("LSP client error for %s: %w", language, err)
	}
	if client == nil {
		return "", fmt.Errorf("no LSP server configured for language: %s", language)
	}

	uri := "file://" + absFile
	rootDir := pool.RootDir()

	// Read file content for DidOpen
	content, err := os.ReadFile(absFile)
	if err != nil {
		return "", fmt.Errorf("failed to read file: %w", err)
	}

	if err := client.DidOpen(uri, language, string(content)); err != nil {
		return "", fmt.Errorf("didOpen failed: %w", err)
	}
	defer client.DidClose(uri)

	// Convert 1-based to 0-based for LSP protocol
	lspLine := line - 1
	lspChar := character - 1
	if lspLine < 0 {
		lspLine = 0
	}
	if lspChar < 0 {
		lspChar = 0
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	switch operation {
	case "go_to_definition":
		raw, err := client.Definition(ctx, uri, lspLine, lspChar)
		if err != nil {
			return "", fmt.Errorf("definition query failed: %w", err)
		}
		return formatLocations(raw, rootDir), nil

	case "find_references":
		raw, err := client.References(ctx, uri, lspLine, lspChar, includeDecl)
		if err != nil {
			return "", fmt.Errorf("references query failed: %w", err)
		}
		return formatLocations(raw, rootDir), nil

	case "hover":
		raw, err := client.Hover(ctx, uri, lspLine, lspChar)
		if err != nil {
			return "", fmt.Errorf("hover query failed: %w", err)
		}
		return formatHover(raw), nil

	case "document_symbols":
		raw, err := client.DocumentSymbol(ctx, uri)
		if err != nil {
			return "", fmt.Errorf("document symbols query failed: %w", err)
		}
		return formatDocumentSymbols(raw), nil

	case "find_implementations":
		raw, err := client.Implementation(ctx, uri, lspLine, lspChar)
		if err != nil {
			return "", fmt.Errorf("implementation query failed: %w", err)
		}
		return formatLocations(raw, rootDir), nil

	case "diagnostics":
		diags := client.WaitForDiagnostics(uri, 3*time.Second)
		relFile := file
		if filepath.IsAbs(file) {
			if rel, err := filepath.Rel(rootDir, file); err == nil {
				relFile = rel
			}
		}
		return formatDiagnostics(diags, relFile), nil
	}

	return "", fmt.Errorf("unhandled operation: %s", operation)
}

func lspWorkspaceSymbols(pool *lsp.Pool, query string) (string, error) {
	if query == "" {
		return "", fmt.Errorf("query parameter is required for workspace_symbols operation")
	}

	rootDir := pool.RootDir()
	var allResults []string

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	for _, lang := range pool.ConfiguredLanguages() {
		client, err := pool.GetClient(lang)
		if err != nil || client == nil {
			continue
		}

		raw, err := client.WorkspaceSymbol(ctx, query)
		if err != nil {
			continue
		}

		results := formatWorkspaceSymbols(raw, rootDir)
		if results != "" && results != "(no results)" {
			allResults = append(allResults, results)
		}
	}

	if len(allResults) == 0 {
		return "(no results)", nil
	}
	return strings.Join(allResults, "\n"), nil
}

// --- Format functions ---

func formatLocations(raw json.RawMessage, rootDir string) string {
	if raw == nil || string(raw) == "null" {
		return "(no results)"
	}

	var lines []string

	// Try []Location
	var locs []struct {
		URI   string `json:"uri"`
		Range struct {
			Start struct {
				Line      int `json:"line"`
				Character int `json:"character"`
			} `json:"start"`
		} `json:"range"`
	}
	if err := json.Unmarshal(raw, &locs); err == nil && len(locs) > 0 {
		for _, l := range locs {
			path := uriToPath(l.URI, rootDir)
			lines = append(lines, fmt.Sprintf("%s:%d:%d", path, l.Range.Start.Line+1, l.Range.Start.Character+1))
			if len(lines) >= 200 {
				lines = append(lines, "... (capped at 200 results)")
				break
			}
		}
		return strings.Join(lines, "\n")
	}

	// Try []LocationLink
	var links []struct {
		TargetURI   string `json:"targetUri"`
		TargetRange struct {
			Start struct {
				Line      int `json:"line"`
				Character int `json:"character"`
			} `json:"start"`
		} `json:"targetRange"`
	}
	if err := json.Unmarshal(raw, &links); err == nil && len(links) > 0 {
		for _, l := range links {
			path := uriToPath(l.TargetURI, rootDir)
			lines = append(lines, fmt.Sprintf("%s:%d:%d", path, l.TargetRange.Start.Line+1, l.TargetRange.Start.Character+1))
			if len(lines) >= 200 {
				lines = append(lines, "... (capped at 200 results)")
				break
			}
		}
		return strings.Join(lines, "\n")
	}

	// Try single Location
	var single struct {
		URI   string `json:"uri"`
		Range struct {
			Start struct {
				Line      int `json:"line"`
				Character int `json:"character"`
			} `json:"start"`
		} `json:"range"`
	}
	if err := json.Unmarshal(raw, &single); err == nil && single.URI != "" {
		path := uriToPath(single.URI, rootDir)
		return fmt.Sprintf("%s:%d:%d", path, single.Range.Start.Line+1, single.Range.Start.Character+1)
	}

	return "(no results)"
}

func formatHover(raw json.RawMessage) string {
	if raw == nil || string(raw) == "null" {
		return "(no hover information)"
	}

	// Hover result: { contents: MarkupContent | MarkedString | MarkedString[] }
	var hover struct {
		Contents json.RawMessage `json:"contents"`
	}
	if err := json.Unmarshal(raw, &hover); err != nil {
		return "(failed to parse hover)"
	}

	// Try MarkupContent { kind, value }
	var markup struct {
		Kind  string `json:"kind"`
		Value string `json:"value"`
	}
	if err := json.Unmarshal(hover.Contents, &markup); err == nil && markup.Value != "" {
		return markup.Value
	}

	// Try plain string
	var plain string
	if err := json.Unmarshal(hover.Contents, &plain); err == nil && plain != "" {
		return plain
	}

	// Try []MarkedString
	var markedStrings []json.RawMessage
	if err := json.Unmarshal(hover.Contents, &markedStrings); err == nil && len(markedStrings) > 0 {
		var parts []string
		for _, ms := range markedStrings {
			var s string
			if json.Unmarshal(ms, &s) == nil {
				parts = append(parts, s)
				continue
			}
			var obj struct {
				Language string `json:"language"`
				Value    string `json:"value"`
			}
			if json.Unmarshal(ms, &obj) == nil {
				parts = append(parts, obj.Value)
			}
		}
		return strings.Join(parts, "\n")
	}

	return "(no hover information)"
}

func formatDocumentSymbols(raw json.RawMessage) string {
	if raw == nil || string(raw) == "null" {
		return "(no symbols)"
	}

	// Try hierarchical DocumentSymbol[]
	type docSym struct {
		Name   string `json:"name"`
		Detail string `json:"detail"`
		Kind   int    `json:"kind"`
		Range  struct {
			Start struct {
				Line int `json:"line"`
			} `json:"start"`
			End struct {
				Line int `json:"line"`
			} `json:"end"`
		} `json:"range"`
		Children []docSym `json:"children"`
	}

	var syms []docSym
	if err := json.Unmarshal(raw, &syms); err == nil && len(syms) > 0 {
		var lines []string
		var flatten func(symbols []docSym, indent int)
		flatten = func(symbols []docSym, indent int) {
			for _, s := range symbols {
				prefix := strings.Repeat("  ", indent)
				kindStr := lsp.SymbolKindName(s.Kind)
				detail := ""
				if s.Detail != "" {
					detail = " " + s.Detail
				}
				lines = append(lines, fmt.Sprintf("%s%s %s%s [L%d-%d]",
					prefix, kindStr, s.Name, detail,
					s.Range.Start.Line+1, s.Range.End.Line+1))
				if len(s.Children) > 0 {
					flatten(s.Children, indent+1)
				}
			}
		}
		flatten(syms, 0)
		return strings.Join(lines, "\n")
	}

	// Try flat SymbolInformation[]
	type symInfo struct {
		Name     string `json:"name"`
		Kind     int    `json:"kind"`
		Location struct {
			Range struct {
				Start struct {
					Line int `json:"line"`
				} `json:"start"`
			} `json:"range"`
		} `json:"location"`
		ContainerName string `json:"containerName"`
	}

	var flatSyms []symInfo
	if err := json.Unmarshal(raw, &flatSyms); err == nil && len(flatSyms) > 0 {
		var lines []string
		for _, s := range flatSyms {
			kindStr := lsp.SymbolKindName(s.Kind)
			container := ""
			if s.ContainerName != "" {
				container = " (" + s.ContainerName + ")"
			}
			lines = append(lines, fmt.Sprintf("%s %s%s L%d",
				kindStr, s.Name, container, s.Location.Range.Start.Line+1))
		}
		return strings.Join(lines, "\n")
	}

	return "(no symbols)"
}

func formatWorkspaceSymbols(raw json.RawMessage, rootDir string) string {
	if raw == nil || string(raw) == "null" {
		return "(no results)"
	}

	type wsSym struct {
		Name     string `json:"name"`
		Kind     int    `json:"kind"`
		Location struct {
			URI   string `json:"uri"`
			Range struct {
				Start struct {
					Line int `json:"line"`
				} `json:"start"`
			} `json:"range"`
		} `json:"location"`
		ContainerName string `json:"containerName"`
	}

	var syms []wsSym
	if err := json.Unmarshal(raw, &syms); err != nil || len(syms) == 0 {
		return "(no results)"
	}

	var lines []string
	for _, s := range syms {
		kindStr := lsp.SymbolKindName(s.Kind)
		path := uriToPath(s.Location.URI, rootDir)
		container := ""
		if s.ContainerName != "" {
			container = " (" + s.ContainerName + ")"
		}
		lines = append(lines, fmt.Sprintf("%s %s%s %s:%d",
			kindStr, s.Name, container, path, s.Location.Range.Start.Line+1))
		if len(lines) >= 100 {
			lines = append(lines, "... (capped at 100 results)")
			break
		}
	}
	return strings.Join(lines, "\n")
}

func formatDiagnostics(diags []lsp.Diagnostic, filePath string) string {
	if len(diags) == 0 {
		return "(no diagnostics)"
	}

	severityNames := map[int]string{
		1: "Error",
		2: "Warning",
		3: "Information",
		4: "Hint",
	}

	var lines []string
	for _, d := range diags {
		sev := severityNames[d.Severity]
		if sev == "" {
			sev = fmt.Sprintf("Severity(%d)", d.Severity)
		}
		lines = append(lines, fmt.Sprintf("%s:%d:%d: [%s] %s",
			filePath, d.Range.Start.Line+1, d.Range.Start.Character+1, sev, d.Message))
	}
	return strings.Join(lines, "\n")
}

// uriToPath converts a file:// URI to a relative path from rootDir, or absolute if outside.
func uriToPath(uri, rootDir string) string {
	if !strings.HasPrefix(uri, "file://") {
		return uri
	}
	absPath := strings.TrimPrefix(uri, "file://")
	if rootDir != "" {
		if rel, err := filepath.Rel(rootDir, absPath); err == nil && !strings.HasPrefix(rel, "..") {
			return rel
		}
	}
	return absPath
}

// getSymbolImpl implements the get_symbol tool. It resolves the symbol by name
// (optionally qualified as "Parent/name") within a specific file or across all
// files via workspace_symbols, then returns the source lines for each match.
//
// name may be:
//   - "FuncName"            — matches any symbol with that name
//   - "TypeName/MethodName" — matches only the named child of a parent
//
// When file is given, only that file is searched (fast, unambiguous).
// When file is empty, every configured LSP language is queried via
// workspace_symbols; matches are re-read from disk to extract their bodies.
func getSymbolImpl(name, file, cwd string) (string, error) {
	pool := lsp.GetPool()
	if pool == nil {
		return "", fmt.Errorf("LSP not available: brain.init has not been run or no LSP servers are configured")
	}

	// Parse optional "Parent/Child" qualification.
	parent, child := parseSymbolName(name)

	if file != "" {
		return getSymbolInFile(pool, parent, child, file, cwd)
	}
	return getSymbolWorkspace(pool, parent, child, cwd)
}

// parseSymbolName splits "Parent/Child" into (parent, child). If there is no
// slash, parent is "" and child is the whole name.
func parseSymbolName(name string) (parent, child string) {
	if idx := strings.LastIndex(name, "/"); idx >= 0 {
		return name[:idx], name[idx+1:]
	}
	return "", name
}

// getSymbolInFile searches a single file for matching symbols and returns their bodies.
func getSymbolInFile(pool *lsp.Pool, parent, child, file, cwd string) (string, error) {
	absFile := file
	if !filepath.IsAbs(file) {
		absFile = filepath.Join(cwd, file)
	}
	if _, err := os.Stat(absFile); os.IsNotExist(err) {
		return "", fmt.Errorf("file not found: %s", file)
	}

	ext := strings.ToLower(filepath.Ext(absFile))
	language := brain.LanguageForExt(ext)
	if language == "" {
		return "", fmt.Errorf("unsupported file type %q — no LSP configured for this extension", ext)
	}

	client, err := pool.GetClient(language)
	if err != nil || client == nil {
		return "", fmt.Errorf("no LSP client available for %s", language)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	syms, err := lsp.ExtractSymbolsCtx(ctx, client, absFile, file, language)
	if err != nil {
		return "", fmt.Errorf("extracting symbols from %s: %w", file, err)
	}

	matches := filterSymbols(syms, parent, child)
	if len(matches) == 0 {
		return fmt.Sprintf("symbol %q not found in %s\nTip: use lsp_query with document_symbols to list all symbols in this file", formatSymbolName(parent, child), file), nil
	}

	return renderSymbolBodies(matches, absFile, file)
}

// getSymbolWorkspace searches all LSP-configured languages via workspace_symbols.
func getSymbolWorkspace(pool *lsp.Pool, parent, child, cwd string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	rootDir := pool.RootDir()
	query := child // workspace_symbols takes a plain name

	type candidate struct {
		sym     lsp.Symbol
		absFile string
		relFile string
	}
	var candidates []candidate

	for _, lang := range pool.ConfiguredLanguages() {
		client, err := pool.GetClient(lang)
		if err != nil || client == nil {
			continue
		}
		raw, err := client.WorkspaceSymbol(ctx, query)
		if err != nil {
			continue
		}
		// Parse workspace symbol results to find matching files/lines.
		type wsSym struct {
			Name          string `json:"name"`
			Kind          int    `json:"kind"`
			ContainerName string `json:"containerName"`
			Location      struct {
				URI   string `json:"uri"`
				Range struct {
					Start struct{ Line int `json:"line"` } `json:"start"`
					End   struct{ Line int `json:"line"` } `json:"end"`
				} `json:"range"`
			} `json:"location"`
		}
		var wsSyms []wsSym
		if err := json.Unmarshal(raw, &wsSyms); err != nil {
			continue
		}
		for _, ws := range wsSyms {
			if !strings.EqualFold(ws.Name, child) {
				continue
			}
			if parent != "" && !strings.EqualFold(ws.ContainerName, parent) {
				continue
			}
			absF := strings.TrimPrefix(ws.Location.URI, "file://")
			relF := uriToPath(ws.Location.URI, rootDir)
			startLine := ws.Location.Range.Start.Line + 1
			endLine := ws.Location.Range.End.Line + 1
			if endLine < startLine {
				endLine = startLine
			}
			candidates = append(candidates, candidate{
				sym: lsp.Symbol{
					Name:      ws.Name,
					Kind:      lsp.SymbolKindName(ws.Kind),
					Parent:    ws.ContainerName,
					StartLine: startLine,
					EndLine:   endLine,
				},
				absFile: absF,
				relFile: relF,
			})
		}
	}

	// Fallback: scan files directly when workspace_symbols returns nothing
	// (some LSP servers don't implement it).
	if len(candidates) == 0 {
		_ = cwd // cwd available for future fallback expansion
		return fmt.Sprintf("symbol %q not found via workspace search\nTip: provide the `file` parameter or use lsp_query with workspace_symbols to see available symbols", formatSymbolName(parent, child)), nil
	}

	var parts []string
	for _, c := range candidates {
		body, err := readLines(c.absFile, c.sym.StartLine, c.sym.EndLine)
		if err != nil {
			parts = append(parts, fmt.Sprintf("// %s:%d-%d (%s)\n// (could not read file: %v)",
				c.relFile, c.sym.StartLine, c.sym.EndLine, c.sym.Kind, err))
			continue
		}
		header := fmt.Sprintf("// %s:%d-%d (%s)", c.relFile, c.sym.StartLine, c.sym.EndLine, c.sym.Kind)
		parts = append(parts, header+"\n"+body)
	}
	return strings.Join(parts, "\n\n"), nil
}

// filterSymbols returns symbols from syms that match parent/child.
// parent="" means match any parent. Matching is case-sensitive by exact name.
func filterSymbols(syms []lsp.Symbol, parent, child string) []lsp.Symbol {
	var out []lsp.Symbol
	for _, s := range syms {
		if s.Name != child {
			continue
		}
		if parent != "" && s.Parent != parent {
			continue
		}
		out = append(out, s)
	}
	return out
}

// renderSymbolBodies reads source lines for each matched symbol and returns
// them as annotated blocks.
func renderSymbolBodies(syms []lsp.Symbol, absFile, relFile string) (string, error) {
	var parts []string
	for _, s := range syms {
		body, err := readLines(absFile, s.StartLine, s.EndLine)
		if err != nil {
			return "", fmt.Errorf("reading %s: %w", relFile, err)
		}
		qualifier := s.Name
		if s.Parent != "" {
			qualifier = s.Parent + "/" + s.Name
		}
		header := fmt.Sprintf("// %s:%d-%d (%s) %s", relFile, s.StartLine, s.EndLine, s.Kind, qualifier)
		parts = append(parts, header+"\n"+body)
	}
	return strings.Join(parts, "\n\n"), nil
}

// readLines returns the 1-based [start, end] lines from a file as a single string.
func readLines(absFile string, start, end int) (string, error) {
	data, err := os.ReadFile(absFile)
	if err != nil {
		return "", err
	}
	lines := strings.Split(string(data), "\n")
	// Clamp to valid range.
	if start < 1 {
		start = 1
	}
	if end > len(lines) {
		end = len(lines)
	}
	if start > end {
		return "", fmt.Errorf("empty range %d-%d", start, end)
	}
	return strings.Join(lines[start-1:end], "\n"), nil
}

// formatSymbolName renders "parent/child" or just "child" for display.
func formatSymbolName(parent, child string) string {
	if parent != "" {
		return parent + "/" + child
	}
	return child
}
