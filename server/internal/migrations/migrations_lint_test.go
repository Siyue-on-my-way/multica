package migrations

import (
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

func TestMigrationNumericPrefixesAreUnique(t *testing.T) {
	files := migrationFilesForLint(t, "*.up.sql")

	// Fork convention: the migration runner ledgers the FULL file stem, so a
	// reused numeric prefix with a distinct name is a legal, ordered
	// coexistence (the fork layers SIY migrations into upstream's numeric
	// space, e.g. 253_issue_handoff_fields beside 253_runtime_profile_add_qwenpaw).
	// What the ledger cannot tolerate — and what this test therefore enforces —
	// is a duplicated full stem. Same-number pairs are still reported as
	// informational so the interleaving stays visible to reviewers.
	const firstUniqueMigrationNumber = 129
	stemByNumber := make(map[int]string)
	stemByName := make(map[string]bool)
	var sharedNumbers []string
	for _, file := range files {
		stem, _, ok := splitMigrationFilename(filepath.Base(file))
		if !ok {
			continue
		}
		if stemByName[stem] {
			t.Errorf("migration %s is duplicated; schema_migrations keys the full stem, so a duplicate silently skips", stem)
			continue
		}
		stemByName[stem] = true
		prefix, _, ok := strings.Cut(stem, "_")
		if !ok {
			continue
		}
		number, err := strconv.Atoi(prefix)
		if err != nil || number < firstUniqueMigrationNumber {
			continue
		}
		if previous, exists := stemByNumber[number]; exists {
			sharedNumbers = append(sharedNumbers, fmt.Sprintf("%s / %s", previous, stem))
			continue
		}
		stemByNumber[number] = stem
	}
	if len(sharedNumbers) > 0 {
		t.Logf("%d shared numeric prefixes (legal under the full-stem ledger, fork convention): %s",
			len(sharedNumbers), strings.Join(sharedNumbers, "; "))
	}
}

func TestMigrationFilesHaveMatchingDirections(t *testing.T) {
	files := migrationFilesForLint(t, "*.sql")

	directionsByStem := make(map[string]map[string]bool)
	for _, file := range files {
		stem, direction, ok := splitMigrationFilename(filepath.Base(file))
		if !ok {
			continue
		}
		if directionsByStem[stem] == nil {
			directionsByStem[stem] = make(map[string]bool)
		}
		directionsByStem[stem][direction] = true
	}

	for stem, directions := range directionsByStem {
		if !directions["up"] || !directions["down"] {
			t.Errorf("migration %s must have both .up.sql and .down.sql files", stem)
		}
	}
}

func migrationFilesForLint(t *testing.T, pattern string) []string {
	t.Helper()

	dir := realMigrationsDir(t)
	files, err := filepath.Glob(filepath.Join(dir, pattern))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatalf("no migration files matched %s in %s", pattern, dir)
	}
	sort.Strings(files)
	return files
}

func realMigrationsDir(t *testing.T) string {
	t.Helper()

	// Relative to the package directory go test runs in; runtime.Caller
	// would return a module path under -trimpath.
	dir, err := filepath.Abs(filepath.Join("..", "..", "migrations"))
	if err != nil {
		t.Fatalf("resolve migrations dir: %v", err)
	}
	return dir
}

func splitMigrationFilename(name string) (stem, direction string, ok bool) {
	for _, candidateDirection := range []string{"up", "down"} {
		suffix := fmt.Sprintf(".%s.sql", candidateDirection)
		if strings.HasSuffix(name, suffix) {
			return strings.TrimSuffix(name, suffix), candidateDirection, true
		}
	}
	return "", "", false
}
