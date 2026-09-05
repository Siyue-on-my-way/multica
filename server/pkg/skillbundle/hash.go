package skillbundle

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"unicode/utf8"
)

const (
	SourceWorkspace = "workspace"
	SourceBuiltin   = "builtin"
	// SourceGlobal marks a skill resolved from the cross-workspace global
	// namespace (migration 264). Global skills ride the same bundle/ref wire
	// shape as workspace skills; the distinct source keeps their cache and
	// allowlist identity separate.
	SourceGlobal = "global"

	// EncodingUTF8 is the default representation for a skill file. It is kept
	// compatible with the original wire shape, where file content lived in the
	// `content` string field and had no encoding marker.
	EncodingUTF8 = "utf8"
	// EncodingBase64 is used for arbitrary bytes that cannot safely travel in a
	// JSON string. The bytes are stored in File.ContentBase64 and are decoded
	// before a bundle is materialised on a runtime.
	EncodingBase64 = "base64"

	// DefaultFileMode is the safe mode used for ordinary skill resources. The
	// executable bit is the only source-file permission that is preserved.
	DefaultFileMode int32 = 0o644
)

type File struct {
	Path            string
	Content         string
	ContentBase64   string
	ContentEncoding string
	Mode            int32
}

type Skill struct {
	ID          string
	Source      string
	Name        string
	Description string
	Content     string
	Files       []File
}

type FileRef struct {
	Path            string
	SHA256          string
	SizeBytes       int64
	ContentEncoding string
	Mode            int32
}

type Manifest struct {
	Hash      string
	SizeBytes int64
	FileCount int
	Files     []FileRef
}

func BuildManifest(skill Skill) Manifest {
	files := append([]File(nil), skill.Files...)
	sort.Slice(files, func(i, j int) bool {
		return files[i].Path < files[j].Path
	})

	h := sha256.New()
	writeHashPart(h, "v1")
	writeHashPart(h, skill.Source)
	writeHashPart(h, skill.ID)
	writeHashPart(h, skill.Name)
	writeHashPart(h, skill.Description)
	writeHashPart(h, skill.Content)

	size := int64(len(skill.Content))
	refs := make([]FileRef, 0, len(files))
	for _, file := range files {
		content, err := FileBytes(file)
		if err != nil {
			// BuildManifest intentionally keeps its historical no-error API. An
			// invalid base64 payload is nevertheless made uncacheable and cannot
			// accidentally validate against the same bytes as a valid file.
			content = []byte("invalid-skill-file:" + err.Error())
		}
		fileHash := sha256.Sum256(content)
		fileDigest := "sha256:" + hex.EncodeToString(fileHash[:])
		// Keep the v1 hash byte-for-byte stable for existing UTF-8, 0644
		// files. This lets an upgraded server and an older daemon share the
		// skill bundle cache during rolling upgrades. Extended metadata is
		// included only when a bundle actually needs it (binary content or an
		// executable file).
		if fileNeedsExtendedManifest(file) {
			writeHashPart(h, "v2")
			writeHashPart(h, normalizedEncoding(file))
			writeHashPart(h, strconv.FormatInt(int64(NormalizeFileMode(file.Mode)), 10))
		}
		writeHashPart(h, file.Path)
		writeHashPart(h, fileDigest)
		writeHashPart(h, string(content))
		size += int64(len(content))
		refs = append(refs, FileRef{
			Path:            file.Path,
			SHA256:          fileDigest,
			SizeBytes:       int64(len(content)),
			ContentEncoding: normalizedEncoding(file),
			Mode:            NormalizeFileMode(file.Mode),
		})
	}

	return Manifest{
		Hash:      "sha256:" + hex.EncodeToString(h.Sum(nil)),
		SizeBytes: size,
		FileCount: len(files),
		Files:     refs,
	}
}

// FileBytes decodes the transport representation of a bundle file. Empty or
// unknown encoding values are treated as UTF-8 for compatibility with bundles
// produced before file metadata existed.
func FileBytes(file File) ([]byte, error) {
	if file.ContentEncoding == "" || file.ContentEncoding == EncodingUTF8 {
		if file.ContentBase64 != "" {
			return nil, fmt.Errorf("utf8 file contains content_base64")
		}
		return []byte(file.Content), nil
	}
	if file.ContentEncoding != EncodingBase64 {
		return nil, fmt.Errorf("unsupported content encoding %q", file.ContentEncoding)
	}
	if file.Content != "" {
		return nil, fmt.Errorf("base64 file contains content")
	}
	decoded, err := base64.StdEncoding.DecodeString(file.ContentBase64)
	if err != nil {
		return nil, fmt.Errorf("decode base64 content: %w", err)
	}
	return decoded, nil
}

// FileFromBytes chooses the smallest safe JSON representation for raw file
// bytes. Valid UTF-8 stays in the legacy `content` field; arbitrary bytes use
// base64 so the byte identity survives JSON, PostgreSQL TEXT, and the daemon
// cache.
func FileFromBytes(path string, content []byte, mode int32) File {
	file := File{Path: path, Mode: NormalizeFileMode(mode)}
	if isUTF8(content) {
		file.Content = string(content)
		file.ContentEncoding = EncodingUTF8
		return file
	}
	file.ContentBase64 = base64.StdEncoding.EncodeToString(content)
	file.ContentEncoding = EncodingBase64
	return file
}

// NormalizeFileMode deliberately preserves only executability. Skill files
// are copied into task-local directories owned by the daemon; carrying over
// arbitrary owner/group/world write bits would be surprising and would make a
// downloaded bundle able to change its own files after hydration.
func NormalizeFileMode(mode int32) int32 {
	if mode == 0 {
		return DefaultFileMode
	}
	if mode&0o111 != 0 {
		return DefaultFileMode | mode&0o111
	}
	return DefaultFileMode
}

func normalizedEncoding(file File) string {
	if file.ContentEncoding == "" {
		return EncodingUTF8
	}
	return file.ContentEncoding
}

func fileNeedsExtendedManifest(file File) bool {
	return file.ContentEncoding == EncodingBase64 || NormalizeFileMode(file.Mode) != DefaultFileMode
}

func isUTF8(content []byte) bool {
	return utf8.Valid(content)
}

func writeHashPart(h interface{ Write([]byte) (int, error) }, value string) {
	_, _ = fmt.Fprintf(h, "%d:%s\n", len(value), value)
}
