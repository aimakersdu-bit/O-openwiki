package scheduler

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// KnowledgeConfig represents custom knowledge configuration optionally stored in .openwikirc
type KnowledgeConfig struct {
	KnowledgeDir string `json:"knowledge_dir"`
	AltDir       string `json:"knowledgeDir"`
}

var h1Regex = regexp.MustCompile(`(?m)^#\s+(.+)$`)

var validAssetExts = map[string]bool{
	".png":  true,
	".jpg":  true,
	".jpeg": true,
	".gif":  true,
	".svg":  true,
	".webp": true,
	".pdf":  true,
}

// FindKnowledgeSourceDir finds the candidate knowledge directory in a repository.
// Precedence:
// 1. .openwikirc explicit "knowledge_dir" configuration
// 2. doc/knowledge/
// 3. docs/
// 4. wiki/
func FindKnowledgeSourceDir(repoPath string) string {
	// 1. Check .openwikirc
	rcPath := filepath.Join(repoPath, ".openwikirc")
	if data, err := os.ReadFile(rcPath); err == nil {
		var cfg KnowledgeConfig
		if err := json.Unmarshal(data, &cfg); err == nil {
			target := cfg.KnowledgeDir
			if target == "" {
				target = cfg.AltDir
			}
			if target != "" {
				full := filepath.Join(repoPath, target)
				if fi, err := os.Stat(full); err == nil && fi.IsDir() {
					return full
				}
			}
		}
	}

	// 2. Candidate default directories
	candidates := []string{
		filepath.Join(repoPath, "doc", "knowledge"),
		filepath.Join(repoPath, "docs"),
		filepath.Join(repoPath, "wiki"),
	}

	for _, c := range candidates {
		if fi, err := os.Stat(c); err == nil && fi.IsDir() {
			return c
		}
	}

	return ""
}

// IngestKnowledgeDocs traverses srcDir, normalizes Markdown files into dstKnowledgeDir,
// and copies static image assets while preserving directory hierarchy.
func IngestKnowledgeDocs(srcDir, dstKnowledgeDir string) (int, error) {
	if err := os.MkdirAll(dstKnowledgeDir, 0755); err != nil {
		return 0, fmt.Errorf("create dstKnowledgeDir: %w", err)
	}

	ingestedCount := 0

	err := filepath.Walk(srcDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			// Skip hidden directories (like .git)
			if strings.HasPrefix(info.Name(), ".") && path != srcDir {
				return filepath.SkipDir
			}
			return nil
		}

		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return nil
		}

		ext := strings.ToLower(filepath.Ext(path))
		dstPath := filepath.Join(dstKnowledgeDir, rel)

		if ext == ".md" {
			// Markdown file: normalize and write
			data, err := os.ReadFile(path)
			if err != nil {
				return nil
			}

			normalized := normalizeMarkdown(string(data), info.Name())
			_ = os.MkdirAll(filepath.Dir(dstPath), 0755)
			if err := os.WriteFile(dstPath, []byte(normalized), 0644); err == nil {
				ingestedCount++
			}
			return nil
		}

		if validAssetExts[ext] {
			// Image or media asset: copy directly
			_ = os.MkdirAll(filepath.Dir(dstPath), 0755)
			_ = copyFile(path, dstPath)
			return nil
		}

		return nil
	})

	return ingestedCount, err
}

// normalizeMarkdown ensures the markdown has an OKF/YAML frontmatter with type: Knowledge
// and an extracted title from the first H1 heading if not already present.
func normalizeMarkdown(content, filename string) string {
	trimmed := strings.TrimSpace(content)
	if strings.HasPrefix(trimmed, "---") {
		// Already has frontmatter, leave as is
		return content
	}

	// Extract title from first H1
	title := strings.TrimSuffix(filename, filepath.Ext(filename))
	if match := h1Regex.FindStringSubmatch(content); len(match) > 1 {
		title = strings.TrimSpace(match[1])
	}

	frontmatter := fmt.Sprintf("---\ntype: Knowledge\ntitle: %s\n---\n\n", title)
	return frontmatter + content
}
