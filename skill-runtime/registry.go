package skillruntime

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

var skillNamePattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

type skill struct {
	Name          string
	Description   string
	Instructions  string
	License       string
	Compatibility string
	AllowedTools  string
	References    map[string]string
	Digest        string
	Source        string
	Stale         bool
	StaleReason   string
}

type rejection struct {
	Path  string `json:"path"`
	Error string `json:"error"`
}

type registrySnapshot struct {
	Generation       uint64
	Skills           map[string]*skill
	Ordered          []*skill
	Rejected         []rejection
	LastRefreshError string
}

type registry struct {
	root     string
	builtins map[string]*skill
	mu       sync.Mutex
	snapshot atomic.Pointer[registrySnapshot]
}

type diskScan struct {
	directories map[string]bool
	valid       map[string]*skill
	invalid     map[string]string
	rejected    []rejection
}

type frontMatter struct {
	Name          string `yaml:"name"`
	Description   string `yaml:"description"`
	License       string `yaml:"license,omitempty"`
	Compatibility string `yaml:"compatibility,omitempty"`
	AllowedTools  string `yaml:"allowed-tools,omitempty"`
}

type addRequest struct {
	Name         string
	Description  string
	Instructions string
	References   map[string]string
}

func newRegistry(root string, builtins map[string]*skill) *registry {
	owned := make(map[string]*skill, len(builtins))
	for name, item := range builtins {
		owned[name] = cloneSkill(item)
	}
	return &registry{root: root, builtins: owned}
}

func (r *registry) Root() string { return r.root }

func (r *registry) Snapshot() *registrySnapshot {
	current := r.snapshot.Load()
	if current == nil {
		return &registrySnapshot{Skills: map[string]*skill{}}
	}
	return current
}

func (r *registry) Refresh(ctx context.Context) error {
	if ctx == nil {
		return errors.New("refresh skill registry: nil context")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.refreshLocked(ctx)
}

func (r *registry) refreshLocked(ctx context.Context) error {
	previous := r.snapshot.Load()
	scan, err := r.scan(ctx)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if previous == nil {
			return err
		}
		next := cloneSnapshot(previous)
		next.LastRefreshError = err.Error()
		r.publish(previous, next)
		return nil
	}
	next := r.merge(previous, scan)
	r.publish(previous, next)
	return nil
}

func (r *registry) publish(previous, next *registrySnapshot) {
	if previous != nil && snapshotsEqual(previous, next) {
		return
	}
	if previous == nil {
		next.Generation = 1
	} else {
		next.Generation = previous.Generation + 1
	}
	r.snapshot.Store(next)
}

func snapshotsEqual(left, right *registrySnapshot) bool {
	if left == nil || right == nil {
		return left == right
	}
	return reflect.DeepEqual(left.Ordered, right.Ordered) &&
		reflect.DeepEqual(left.Rejected, right.Rejected) &&
		left.LastRefreshError == right.LastRefreshError
}

func cloneSnapshot(source *registrySnapshot) *registrySnapshot {
	result := &registrySnapshot{
		Generation: source.Generation, Skills: make(map[string]*skill, len(source.Skills)),
		Ordered: make([]*skill, 0, len(source.Ordered)), Rejected: append([]rejection(nil), source.Rejected...),
		LastRefreshError: source.LastRefreshError,
	}
	for _, item := range source.Ordered {
		copy := cloneSkill(item)
		result.Ordered = append(result.Ordered, copy)
		result.Skills[copy.Name] = copy
	}
	return result
}

func cloneSkill(source *skill) *skill {
	if source == nil {
		return nil
	}
	copy := *source
	copy.References = make(map[string]string, len(source.References))
	for name, content := range source.References {
		copy.References[name] = content
	}
	return &copy
}

func (r *registry) scan(ctx context.Context) (diskScan, error) {
	root, err := os.OpenRoot(r.root)
	if err != nil {
		return diskScan{}, fmt.Errorf("open skill root: %w", err)
	}
	defer root.Close()
	directory, err := root.Open(".")
	if err != nil {
		return diskScan{}, fmt.Errorf("open skill directory: %w", err)
	}
	entries, err := directory.ReadDir(-1)
	closeErr := directory.Close()
	if err != nil {
		return diskScan{}, fmt.Errorf("read skill directory: %w", err)
	}
	if closeErr != nil {
		return diskScan{}, fmt.Errorf("close skill directory: %w", closeErr)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	result := diskScan{
		directories: make(map[string]bool), valid: make(map[string]*skill), invalid: make(map[string]string),
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return diskScan{}, err
		}
		name := entry.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		info, err := root.Lstat(name)
		if err != nil {
			result.rejected = append(result.rejected, rejection{Path: name, Error: err.Error()})
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			result.rejected = append(result.rejected, rejection{Path: name, Error: "entry is not a regular directory"})
			continue
		}
		result.directories[name] = true
		if _, reserved := r.builtins[name]; reserved {
			message := "name is reserved by a built-in Skill"
			result.invalid[name] = message
			result.rejected = append(result.rejected, rejection{Path: name, Error: message})
			continue
		}
		loaded, err := loadSkill(ctx, root, name)
		if err != nil {
			message := err.Error()
			result.invalid[name] = message
			result.rejected = append(result.rejected, rejection{Path: name, Error: message})
			continue
		}
		result.valid[name] = loaded
	}
	return result, nil
}

func (r *registry) merge(previous *registrySnapshot, scan diskScan) *registrySnapshot {
	result := &registrySnapshot{Skills: make(map[string]*skill), Rejected: append([]rejection(nil), scan.rejected...)}
	admitted := make(map[string]bool)
	catalogBytes := 0
	admit := func(candidate *skill) bool {
		entryBytes := catalogEntryBytes(candidate)
		if len(result.Ordered) >= maxSkills || catalogBytes+entryBytes > maxCatalogBytes {
			return false
		}
		copy := cloneSkill(candidate)
		result.Skills[copy.Name] = copy
		result.Ordered = append(result.Ordered, copy)
		admitted[copy.Name] = true
		catalogBytes += entryBytes
		return true
	}
	builtinNames := make([]string, 0, len(r.builtins))
	for name := range r.builtins {
		builtinNames = append(builtinNames, name)
	}
	sort.Strings(builtinNames)
	for _, name := range builtinNames {
		if !admit(r.builtins[name]) {
			result.Rejected = append(result.Rejected, rejection{Path: name, Error: ErrSkillLimit.Error()})
		}
	}

	if previous != nil {
		for _, old := range previous.Ordered {
			if admitted[old.Name] {
				continue
			}
			if !scan.directories[old.Name] {
				continue
			}
			if current, ok := scan.valid[old.Name]; ok {
				if admit(current) {
					continue
				}
				stale := cloneSkill(old)
				stale.Stale = true
				stale.StaleReason = ErrSkillLimit.Error()
				if admit(stale) {
					result.Rejected = append(result.Rejected, rejection{Path: old.Name, Error: ErrSkillLimit.Error()})
				}
				continue
			}
			stale := cloneSkill(old)
			stale.Stale = true
			stale.StaleReason = scan.invalid[old.Name]
			_ = admit(stale)
		}
	}

	names := make([]string, 0, len(scan.valid))
	for name := range scan.valid {
		if !admitted[name] {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		if !admit(scan.valid[name]) {
			result.Rejected = append(result.Rejected, rejection{Path: name, Error: ErrSkillLimit.Error()})
		}
	}
	sort.Slice(result.Ordered, func(i, j int) bool { return result.Ordered[i].Name < result.Ordered[j].Name })
	sort.Slice(result.Rejected, func(i, j int) bool {
		if result.Rejected[i].Path != result.Rejected[j].Path {
			return result.Rejected[i].Path < result.Rejected[j].Path
		}
		return result.Rejected[i].Error < result.Rejected[j].Error
	})
	return result
}

func loadSkill(ctx context.Context, root *os.Root, directory string) (*skill, error) {
	manifestPath := path.Join(directory, "SKILL.md")
	info, err := root.Lstat(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("SKILL.md: %w: %w", err, ErrInvalidSkill)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("SKILL.md is not a regular file: %w", ErrInvalidSkill)
	}
	if info.Size() > maxSkillBytes {
		return nil, fmt.Errorf("SKILL.md exceeds %d bytes: %w", maxSkillBytes, ErrSkillLimit)
	}
	raw, err := readRootFile(root, manifestPath)
	if err != nil {
		return nil, fmt.Errorf("read SKILL.md: %w", err)
	}
	metadata, instructions, err := parseSkill(raw, directory)
	if err != nil {
		return nil, err
	}
	references, referenceBytes, err := loadReferences(ctx, root, directory)
	if err != nil {
		return nil, err
	}
	if len(raw)+referenceBytes > maxSkillTotalBytes {
		return nil, fmt.Errorf("skill content exceeds %d bytes: %w", maxSkillTotalBytes, ErrSkillLimit)
	}
	loaded := &skill{
		Name: metadata.Name, Description: metadata.Description, Instructions: instructions,
		License: metadata.License, Compatibility: metadata.Compatibility, AllowedTools: metadata.AllowedTools,
		References: references, Source: skillSourceState,
	}
	loaded.Digest = skillDigest(raw, references)
	return loaded, nil
}

func parseSkill(raw []byte, directory string) (frontMatter, string, error) {
	if len(raw) > maxSkillBytes {
		return frontMatter{}, "", fmt.Errorf("SKILL.md exceeds %d bytes: %w", maxSkillBytes, ErrSkillLimit)
	}
	if !utf8.Valid(raw) {
		return frontMatter{}, "", fmt.Errorf("SKILL.md is not valid UTF-8: %w", ErrInvalidSkill)
	}
	header, body, err := splitFrontMatter(raw)
	if err != nil {
		return frontMatter{}, "", err
	}
	var metadata frontMatter
	if err := yaml.Unmarshal(header, &metadata); err != nil {
		return frontMatter{}, "", fmt.Errorf("parse frontmatter: %w: %w", err, ErrInvalidSkill)
	}
	if err := validateMetadata(metadata, directory); err != nil {
		return frontMatter{}, "", err
	}
	instructions := string(body)
	if strings.TrimSpace(instructions) == "" {
		return frontMatter{}, "", fmt.Errorf("instructions must be non-empty: %w", ErrInvalidSkill)
	}
	return metadata, instructions, nil
}

func splitFrontMatter(raw []byte) ([]byte, []byte, error) {
	line, next := nextLine(raw, 0)
	if string(bytes.TrimSuffix(line, []byte("\r"))) != "---" {
		return nil, nil, fmt.Errorf("SKILL.md must start with YAML frontmatter: %w", ErrInvalidSkill)
	}
	headerStart := next
	for next <= len(raw) {
		lineStart := next
		line, after := nextLine(raw, next)
		if string(bytes.TrimSuffix(line, []byte("\r"))) == "---" {
			return raw[headerStart:lineStart], raw[after:], nil
		}
		if after == next {
			break
		}
		next = after
	}
	return nil, nil, fmt.Errorf("SKILL.md frontmatter is not closed: %w", ErrInvalidSkill)
}

func nextLine(raw []byte, start int) ([]byte, int) {
	if start >= len(raw) {
		return raw[start:], start
	}
	index := bytes.IndexByte(raw[start:], '\n')
	if index < 0 {
		return raw[start:], len(raw)
	}
	end := start + index
	return raw[start:end], end + 1
}

func validateMetadata(metadata frontMatter, directory string) error {
	if !skillNamePattern.MatchString(metadata.Name) || len(metadata.Name) > 64 {
		return fmt.Errorf("name must contain 1-64 lowercase letters, digits, or separated hyphens: %w", ErrInvalidSkill)
	}
	if metadata.Name != directory {
		return fmt.Errorf("frontmatter name %q does not match directory %q: %w", metadata.Name, directory, ErrInvalidSkill)
	}
	if metadata.Description == "" || !utf8.ValidString(metadata.Description) || utf8.RuneCountInString(metadata.Description) > 1024 {
		return fmt.Errorf("description must be non-empty UTF-8 with at most 1024 characters: %w", ErrInvalidSkill)
	}
	for field, value := range map[string]string{
		"license": metadata.License, "compatibility": metadata.Compatibility, "allowed-tools": metadata.AllowedTools,
	} {
		if !utf8.ValidString(value) {
			return fmt.Errorf("%s is not valid UTF-8: %w", field, ErrInvalidSkill)
		}
	}
	return nil
}

func loadReferences(ctx context.Context, root *os.Root, directory string) (map[string]string, int, error) {
	result := make(map[string]string)
	referencesDir := path.Join(directory, "references")
	info, err := root.Lstat(referencesDir)
	if errors.Is(err, fs.ErrNotExist) {
		return result, 0, nil
	}
	if err != nil {
		return nil, 0, fmt.Errorf("inspect references: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, 0, fmt.Errorf("references is not a regular directory: %w", ErrInvalidSkill)
	}
	total := 0
	if err := walkReferences(ctx, root, referencesDir, "", 0, result, &total); err != nil {
		return nil, 0, err
	}
	return result, total, nil
}

func walkReferences(ctx context.Context, root *os.Root, diskDirectory, relative string, depth int, result map[string]string, total *int) error {
	if depth > maxReferenceDepth {
		return fmt.Errorf("reference nesting exceeds %d levels: %w", maxReferenceDepth, ErrSkillLimit)
	}
	directory, err := root.Open(diskDirectory)
	if err != nil {
		return fmt.Errorf("open references: %w", err)
	}
	entries, err := directory.ReadDir(-1)
	closeErr := directory.Close()
	if err != nil {
		return fmt.Errorf("read references: %w", err)
	}
	if closeErr != nil {
		return fmt.Errorf("close references: %w", closeErr)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		name := entry.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		relativeName := path.Join(relative, name)
		diskName := path.Join(diskDirectory, name)
		info, err := root.Lstat(diskName)
		if err != nil {
			return fmt.Errorf("inspect reference %q: %w", relativeName, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("reference %q is a symbolic link: %w", relativeName, ErrInvalidSkill)
		}
		if info.IsDir() {
			if err := walkReferences(ctx, root, diskName, relativeName, depth+1, result, total); err != nil {
				return err
			}
			continue
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("reference %q is not a regular file: %w", relativeName, ErrInvalidSkill)
		}
		if len(result) >= maxReferenceFiles {
			return fmt.Errorf("references exceed %d files: %w", maxReferenceFiles, ErrSkillLimit)
		}
		if info.Size() > maxReferenceBytes {
			return fmt.Errorf("reference %q exceeds %d bytes: %w", relativeName, maxReferenceBytes, ErrSkillLimit)
		}
		raw, err := readRootFile(root, diskName)
		if err != nil {
			return fmt.Errorf("read reference %q: %w", relativeName, err)
		}
		if !utf8.Valid(raw) {
			return fmt.Errorf("reference %q is not valid UTF-8: %w", relativeName, ErrInvalidSkill)
		}
		result[relativeName] = string(raw)
		*total += len(raw)
	}
	return nil
}

func skillDigest(manifest []byte, references map[string]string) string {
	digest := sha256.New()
	_, _ = digest.Write(manifest)
	names := sortedReferenceNames(references)
	for _, name := range names {
		_, _ = io.WriteString(digest, "\x00"+name+"\x00"+references[name])
	}
	return "sha256:" + hex.EncodeToString(digest.Sum(nil))
}

func sortedReferenceNames(references map[string]string) []string {
	names := make([]string, 0, len(references))
	for name := range references {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func catalogEntryBytes(item *skill) int {
	return len(item.Name) + len(strconv.Quote(item.Description)) + len("- ``: \n")
}

func (r *registry) Add(ctx context.Context, request addRequest) (*skill, error) {
	if ctx == nil {
		return nil, errors.New("add skill: nil context")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	manifest, normalized, err := prepareAdd(request)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.refreshLocked(ctx); err != nil {
		return nil, err
	}
	current := r.snapshot.Load()
	if _, exists := current.Skills[request.Name]; exists {
		return nil, ErrSkillExists
	}
	if len(current.Ordered) >= maxSkills || catalogSize(current)+catalogEntryBytes(normalized) > maxCatalogBytes {
		return nil, ErrSkillLimit
	}
	root, err := os.OpenRoot(r.root)
	if err != nil {
		return nil, fmt.Errorf("open skill root: %w", err)
	}
	defer root.Close()
	if _, err := root.Lstat(request.Name); err == nil {
		return nil, ErrSkillExists
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("inspect target skill: %w", err)
	}
	if err := root.Mkdir(request.Name, 0o700); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return nil, ErrSkillExists
		}
		return nil, fmt.Errorf("create skill directory: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = removeRootTree(root, request.Name)
		}
	}()
	if len(request.References) != 0 {
		if err := root.Mkdir(path.Join(request.Name, "references"), 0o700); err != nil {
			return nil, fmt.Errorf("create references directory: %w", err)
		}
		for _, name := range sortedReferenceNames(request.References) {
			target := path.Join(request.Name, "references", name)
			if parent := path.Dir(target); parent != "." {
				if err := mkdirRootAll(root, parent, 0o700); err != nil {
					return nil, fmt.Errorf("create reference directory: %w", err)
				}
			}
			if err := writeAtomic(root, target, []byte(request.References[name])); err != nil {
				return nil, fmt.Errorf("write reference %q: %w", name, err)
			}
		}
	}
	if err := writeAtomic(root, path.Join(request.Name, "SKILL.md"), manifest); err != nil {
		return nil, fmt.Errorf("write SKILL.md: %w", err)
	}
	committed = true
	if err := r.refreshLocked(ctx); err != nil {
		return nil, err
	}
	created, ok := r.snapshot.Load().Skills[request.Name]
	if !ok {
		return nil, fmt.Errorf("created skill was not published: %w", ErrInvalidSkill)
	}
	return cloneSkill(created), nil
}

func prepareAdd(request addRequest) ([]byte, *skill, error) {
	metadata := frontMatter{Name: request.Name, Description: request.Description}
	if err := validateMetadata(metadata, request.Name); err != nil {
		return nil, nil, err
	}
	if !utf8.ValidString(request.Instructions) || strings.TrimSpace(request.Instructions) == "" {
		return nil, nil, fmt.Errorf("instructions must be non-empty UTF-8: %w", ErrInvalidSkill)
	}
	normalizedReferences := make(map[string]string, len(request.References))
	total := 0
	for name, value := range request.References {
		normalized, err := validateReferencePath(name)
		if err != nil {
			return nil, nil, err
		}
		if normalized != name {
			return nil, nil, fmt.Errorf("reference path %q is not canonical: %w", name, ErrInvalidSkill)
		}
		if !utf8.ValidString(value) {
			return nil, nil, fmt.Errorf("reference %q is not valid UTF-8: %w", name, ErrInvalidSkill)
		}
		if len(value) > maxReferenceBytes {
			return nil, nil, fmt.Errorf("reference %q exceeds %d bytes: %w", name, maxReferenceBytes, ErrSkillLimit)
		}
		if _, duplicate := normalizedReferences[name]; duplicate {
			return nil, nil, fmt.Errorf("duplicate reference %q: %w", name, ErrInvalidSkill)
		}
		normalizedReferences[name] = value
		total += len(value)
	}
	if len(normalizedReferences) > maxReferenceFiles {
		return nil, nil, fmt.Errorf("references exceed %d files: %w", maxReferenceFiles, ErrSkillLimit)
	}
	header, err := yaml.Marshal(metadata)
	if err != nil {
		return nil, nil, fmt.Errorf("encode frontmatter: %w", err)
	}
	manifest := append([]byte("---\n"), header...)
	manifest = append(manifest, []byte("---\n\n")...)
	manifest = append(manifest, request.Instructions...)
	if len(manifest) > maxSkillBytes {
		return nil, nil, fmt.Errorf("SKILL.md exceeds %d bytes: %w", maxSkillBytes, ErrSkillLimit)
	}
	if len(manifest)+total > maxSkillTotalBytes {
		return nil, nil, fmt.Errorf("skill content exceeds %d bytes: %w", maxSkillTotalBytes, ErrSkillLimit)
	}
	loaded := &skill{
		Name: request.Name, Description: request.Description, Instructions: request.Instructions,
		References: normalizedReferences, Source: skillSourceState,
	}
	loaded.Digest = skillDigest(manifest, normalizedReferences)
	return manifest, loaded, nil
}

func validateReferencePath(value string) (string, error) {
	if value == "" || !utf8.ValidString(value) || strings.Contains(value, `\`) || path.IsAbs(value) {
		return "", fmt.Errorf("reference path %q is invalid: %w", value, ErrInvalidSkill)
	}
	clean := path.Clean(value)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || clean != value {
		return "", fmt.Errorf("reference path %q is unsafe: %w", value, ErrInvalidSkill)
	}
	segments := strings.Split(clean, "/")
	if len(segments) > maxReferenceDepth+1 {
		return "", fmt.Errorf("reference path %q exceeds %d levels: %w", value, maxReferenceDepth, ErrSkillLimit)
	}
	for _, segment := range segments {
		if segment == "" || segment == "." || segment == ".." || strings.HasPrefix(segment, ".") {
			return "", fmt.Errorf("reference path %q contains an invalid segment: %w", value, ErrInvalidSkill)
		}
	}
	return clean, nil
}

func writeAtomic(root *os.Root, target string, data []byte) error {
	suffix := make([]byte, 8)
	if _, err := rand.Read(suffix); err != nil {
		return err
	}
	temporary := path.Join(path.Dir(target), ".tmp-"+hex.EncodeToString(suffix))
	file, err := root.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	closed := false
	defer func() {
		if !closed {
			_ = file.Close()
		}
		_ = root.Remove(temporary)
	}()
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	closed = true
	return os.Rename(
		filepath.Join(root.Name(), filepath.FromSlash(temporary)),
		filepath.Join(root.Name(), filepath.FromSlash(target)),
	)
}

func catalogSize(snapshot *registrySnapshot) int {
	total := 0
	for _, item := range snapshot.Ordered {
		total += catalogEntryBytes(item)
	}
	return total
}

func readRootFile(root *os.Root, name string) ([]byte, error) {
	file, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	raw, readErr := io.ReadAll(file)
	closeErr := file.Close()
	if readErr != nil {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	return raw, nil
}

func mkdirRootAll(root *os.Root, name string, mode os.FileMode) error {
	clean := path.Clean(name)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || path.IsAbs(clean) {
		return fmt.Errorf("unsafe directory path %q", name)
	}
	current := ""
	for _, segment := range strings.Split(clean, "/") {
		if current == "" {
			current = segment
		} else {
			current = path.Join(current, segment)
		}
		if err := root.Mkdir(current, mode); err != nil {
			if !errors.Is(err, fs.ErrExist) {
				return err
			}
			info, statErr := root.Lstat(current)
			if statErr != nil {
				return statErr
			}
			if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
				return fmt.Errorf("%q is not a regular directory", current)
			}
		}
	}
	return nil
}

func removeRootTree(root *os.Root, name string) error {
	info, err := root.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return root.Remove(name)
	}
	directory, err := root.Open(name)
	if err != nil {
		return err
	}
	entries, readErr := directory.ReadDir(-1)
	closeErr := directory.Close()
	if readErr != nil {
		return readErr
	}
	if closeErr != nil {
		return closeErr
	}
	for _, entry := range entries {
		if err := removeRootTree(root, path.Join(name, entry.Name())); err != nil {
			return err
		}
	}
	return root.Remove(name)
}
