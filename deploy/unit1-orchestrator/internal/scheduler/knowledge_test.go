package scheduler

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFindKnowledgeSourceDir_DefaultCandidates(t *testing.T) {
	// Case 1: doc/knowledge takes first precedence
	tmp1 := t.TempDir()
	_ = os.MkdirAll(filepath.Join(tmp1, "doc", "knowledge"), 0755)
	_ = os.MkdirAll(filepath.Join(tmp1, "docs"), 0755)
	found1 := FindKnowledgeSourceDir(tmp1)
	if found1 != filepath.Join(tmp1, "doc", "knowledge") {
		t.Fatalf("expected doc/knowledge, got %s", found1)
	}

	// Case 2: docs fallback when doc/knowledge is absent
	tmp2 := t.TempDir()
	_ = os.MkdirAll(filepath.Join(tmp2, "docs"), 0755)
	_ = os.MkdirAll(filepath.Join(tmp2, "wiki"), 0755)
	found2 := FindKnowledgeSourceDir(tmp2)
	if found2 != filepath.Join(tmp2, "docs") {
		t.Fatalf("expected docs, got %s", found2)
	}

	// Case 3: wiki fallback
	tmp3 := t.TempDir()
	_ = os.MkdirAll(filepath.Join(tmp3, "wiki"), 0755)
	found3 := FindKnowledgeSourceDir(tmp3)
	if found3 != filepath.Join(tmp3, "wiki") {
		t.Fatalf("expected wiki, got %s", found3)
	}

	// Case 4: None exists
	tmp4 := t.TempDir()
	found4 := FindKnowledgeSourceDir(tmp4)
	if found4 != "" {
		t.Fatalf("expected empty, got %s", found4)
	}
}

func TestFindKnowledgeSourceDir_ConfigOverride(t *testing.T) {
	tmp := t.TempDir()
	customDir := filepath.Join(tmp, "my-custom-notes")
	_ = os.MkdirAll(customDir, 0755)

	// create .openwikirc with custom directory
	rcContent := `{"knowledge_dir": "my-custom-notes"}`
	_ = os.WriteFile(filepath.Join(tmp, ".openwikirc"), []byte(rcContent), 0644)

	found := FindKnowledgeSourceDir(tmp)
	if found != customDir {
		t.Fatalf("expected %s from .openwikirc, got %s", customDir, found)
	}
}

func TestIngestKnowledgeDocs_NormalizesAndCopies(t *testing.T) {
	tmp := t.TempDir()
	srcDir := filepath.Join(tmp, "docs")
	_ = os.MkdirAll(filepath.Join(srcDir, "sub"), 0755)

	// Doc 1: Has no frontmatter, only H1 heading
	doc1Path := filepath.Join(srcDir, "intro.md")
	_ = os.WriteFile(doc1Path, []byte("# 核心架构说明\n\n这是正文内容。"), 0644)

	// Doc 2: Already has frontmatter
	doc2Path := filepath.Join(srcDir, "sub", "details.md")
	doc2Content := "---\ntype: Architecture\ntitle: 详情设计\n---\n\n## 子系统"
	_ = os.WriteFile(doc2Path, []byte(doc2Content), 0644)

	// Asset 1: Image file
	assetDir := filepath.Join(srcDir, "images")
	_ = os.MkdirAll(assetDir, 0755)
	imgPath := filepath.Join(assetDir, "arch.png")
	_ = os.WriteFile(imgPath, []byte("fake-png-bytes"), 0644)

	// Ignored file: .txt
	txtPath := filepath.Join(srcDir, "todo.txt")
	_ = os.WriteFile(txtPath, []byte("something"), 0644)

	dstKnowledgeDir := filepath.Join(tmp, "openwiki", "knowledge")
	count, err := IngestKnowledgeDocs(srcDir, dstKnowledgeDir)
	if err != nil {
		t.Fatalf("IngestKnowledgeDocs failed: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected 2 markdown files ingested, got %d", count)
	}

	// Verify doc1 normalization
	dstDoc1 := filepath.Join(dstKnowledgeDir, "intro.md")
	data1, err := os.ReadFile(dstDoc1)
	if err != nil {
		t.Fatalf("read dstDoc1: %v", err)
	}
	s1 := string(data1)
	if !strings.Contains(s1, "type: Knowledge") {
		t.Errorf("doc1 should have type: Knowledge added, got:\n%s", s1)
	}
	if !strings.Contains(s1, "title: 核心架构说明") {
		t.Errorf("doc1 should extract title, got:\n%s", s1)
	}

	// Verify doc2 preserved frontmatter
	dstDoc2 := filepath.Join(dstKnowledgeDir, "sub", "details.md")
	data2, err := os.ReadFile(dstDoc2)
	if err != nil {
		t.Fatalf("read dstDoc2: %v", err)
	}
	s2 := string(data2)
	if !strings.Contains(s2, "type: Architecture") {
		t.Errorf("doc2 should preserve its original type, got:\n%s", s2)
	}

	// Verify image asset copied
	dstImg := filepath.Join(dstKnowledgeDir, "images", "arch.png")
	if _, err := os.Stat(dstImg); err != nil {
		t.Errorf("image asset should be copied to %s", dstImg)
	}

	// Verify ignored file not copied
	dstTxt := filepath.Join(dstKnowledgeDir, "todo.txt")
	if _, err := os.Stat(dstTxt); !os.IsNotExist(err) {
		t.Errorf("txt file should not be copied to %s", dstTxt)
	}
}

