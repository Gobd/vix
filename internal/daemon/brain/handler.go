package brain

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/get-vix/vix/internal/config"
	"github.com/get-vix/vix/internal/daemon/brain/lsp"
)

var daemonCtx context.Context

// RegisterBrainHandlers registers brain.* command handlers with the daemon.
func RegisterBrainHandlers(register func(string, func(map[string]any) (map[string]any, error)), cred config.Credential, ctx context.Context) {
	daemonCtx = ctx
	register("brain.init", func(data map[string]any) (map[string]any, error) {
		return doBrainInit(data, cred)
	})
	register("brain.update_files", func(data map[string]any) (map[string]any, error) {
		return doBrainUpdateFiles(data, cred)
	})
}

func doBrainInit(data map[string]any, cred config.Credential) (map[string]any, error) {
	params, _ := data["params"].(map[string]any)
	projectPath, _ := params["project_path"].(string)
	if projectPath == "" {
		projectPath = "."
	}
	root, _ := filepath.Abs(projectPath)

	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return map[string]any{"status": "error", "message": fmt.Sprintf("Not a directory: %s", root)}, nil
	}

	// Resolve brain directory from the caller, falling back to the legacy
	// cwd/.vix layout if unset.
	brainDir, _ := params["brain_dir"].(string)
	if brainDir == "" {
		brainDir = filepath.Join(root, ".vix")
	}
	os.MkdirAll(brainDir, 0o755)

	// Resolve the languages.json paths to consult for the ext→language map and
	// LSP server configs, falling back to the home-level config/languages.json
	// if the caller did not supply them. Languages are home-only (not layered
	// with the project), so this is normally a single path.
	var languagesPaths []string
	if raw, ok := params["languages_paths"].([]string); ok {
		languagesPaths = raw
	} else if raw, ok := params["languages_paths"].([]any); ok {
		for _, v := range raw {
			if s, ok := v.(string); ok {
				languagesPaths = append(languagesPaths, s)
			}
		}
	}
	if len(languagesPaths) == 0 {
		home := config.HomeVixDir()
		if home != "" {
			languagesPaths = append(languagesPaths, filepath.Join(home, "config", "languages.json"))
		}
	}

	// Load language→extension map and initialize LSP pool
	InitLanguageMap(languagesPaths)
	lsp.InitPool(daemonCtx, root, languagesPaths...)

	LogInfo("Brain init complete for %s", root)

	return map[string]any{
		"status": "ok",
		"data": map[string]any{
			"project_name": filepath.Base(root),
			"brain_path":   brainDir,
		},
	}, nil
}

func doBrainUpdateFiles(data map[string]any, cred config.Credential) (map[string]any, error) {
	start := time.Now()
	params, _ := data["params"].(map[string]any)

	var files []string
	if raw, ok := params["files"].([]string); ok {
		files = raw
	} else if raw, ok := params["files"].([]any); ok {
		for _, v := range raw {
			if s, ok := v.(string); ok {
				files = append(files, s)
			}
		}
	}

	root, _ := params["project_path"].(string)
	if root == "" {
		root = "."
	}
	root, _ = filepath.Abs(root)

	pool := lsp.GetPool()
	updated := 0

	for _, f := range files {
		abs := f
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(root, f)
		}
		data, err := os.ReadFile(abs)
		if err != nil {
			continue
		}

		rel, _ := filepath.Rel(root, abs)
		lang := LanguageForExt(filepath.Ext(abs))
		if lang == "" || pool == nil {
			updated++
			continue
		}

		client, err := pool.GetClient(lang)
		if err != nil {
			updated++
			continue
		}

		uri := "file://" + abs
		langID := lang
		// DidOpen refreshes the LSP server's view of the file so subsequent
		// symbol/definition queries see the current content.
		_ = client.DidOpen(uri, langID, string(data))
		_ = rel
		updated++
	}

	return map[string]any{
		"status": "ok",
		"data": map[string]any{
			"updated_files":    updated,
			"duration_seconds": time.Since(start).Seconds(),
		},
	}, nil
}
