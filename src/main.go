package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	_ "modernc.org/sqlite"
)

const (
	uuidNamespace                      = "6494cb59-e2bc-4adc-bb65-af25df95a72e"
	driverName                         = "sqlite"
	searchDeduplicationFetchMultiplier = 3
	programDirectoryName               = "ai-memory"
	schemaDirectoryName                = "schema"
	schemaFileName                     = "schema.sql"
	logDirectoryName                   = "logs"
	errorLogFileName                   = "index-errors.log"
)

var (
	topLevelRE   = regexp.MustCompile(`^([A-Za-z0-9_-]+):(?:[ \t]*(.*))?$`)
	closingRE    = regexp.MustCompile(`(?m)^---[ \t]*(?:\r?\n|$)`)
	requiredKeys = []string{
		"title", "description", "type", "status", "index", "canonical_key", "tags",
	}
	allowedStatuses = map[string]bool{
		"draft": true, "active": true, "archived": true, "obsolete": true,
	}
	wikiLinkRE      = regexp.MustCompile(`\[\[([^\]|#]+)(?:#[^\]|]+)?(?:\|[^\]]+)?\]\]`)
	htmlCommentRE   = regexp.MustCompile(`(?is)<!--.*?-->`)
	htmlHiddenRE    = regexp.MustCompile(`(?is)<(?:script|style|template)\b[^>]*>.*?</(?:script|style|template)\s*>`)
	htmlTitleRE     = regexp.MustCompile(`(?is)<title\b[^>]*>(.*?)</title\s*>`)
	htmlMetaRE      = regexp.MustCompile(`(?is)<meta\b[^>]*>`)
	htmlAttributeRE = regexp.MustCompile(`(?is)([a-z_:][-a-z0-9_:.]*)\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s"'=<>]+))`)
	htmlBlockRE     = regexp.MustCompile(`(?is)</?(?:address|article|aside|blockquote|br|caption|dd|div|dl|dt|figcaption|figure|footer|h[1-6]|header|li|main|nav|ol|p|pre|section|table|td|th|tr|ul)\b[^>]*>`)
	htmlTagRE       = regexp.MustCompile(`(?is)<[^>]+>`)
)

type paths struct {
	executable string
	vaultRoot  string
	database   string
	schema     string
	dirty      string
	errorLog   string
}

type metadata struct {
	values map[string]any
	order  []string
}

func newMetadata() metadata {
	return metadata{values: make(map[string]any)}
}

func (m *metadata) set(key string, value any) {
	if _, exists := m.values[key]; !exists {
		m.order = append(m.order, key)
	}
	m.values[key] = value
}

type note struct {
	path         string
	title        string
	description  string
	itemType     string
	itemSubtype  string
	project      string
	area         string
	status       string
	priority     string
	canonicalKey string
	tags         []string
	sourceTool   string
	sourceURL    string
	repoPath     string
	body         string
	sourceHash   string
	index        bool
	metadata     metadata
}

type counts struct {
	Scanned             int `json:"scanned"`
	Inserted            int `json:"inserted"`
	Updated             int `json:"updated"`
	Unchanged           int `json:"unchanged"`
	Obsoleted           int `json:"obsoleted"`
	StatementsObsoleted int `json:"statements_obsoleted"`
}

type existingRow struct {
	id           string
	sourceHash   string
	sourcePath   string
	status       string
	canonicalKey string
	body         string
	createdAt    string
}

type connection struct {
	db   *sql.DB
	conn *sql.Conn
}

func (c *connection) close() {
	if c.conn != nil {
		_ = c.conn.Close()
	}
	if c.db != nil {
		_ = c.db.Close()
	}
}

type options struct {
	changed         bool
	full            bool
	ifDirty         bool
	check           bool
	markDirty       bool
	search          string
	searchSet       bool
	installHooks    bool
	includeArchived bool
	limit           int
	hook            bool
}

type checkResult struct {
	Integrity              string              `json:"integrity"`
	ForeignKeyErrors       int                 `json:"foreign_key_errors"`
	SourceFiles            int                 `json:"source_files"`
	IndexableNotes         int                 `json:"indexable_notes"`
	ActiveSourceRows       int                 `json:"active_source_rows"`
	MissingRows            []string            `json:"missing_rows"`
	MismatchedRows         []string            `json:"mismatched_rows"`
	DuplicateCanonicalKeys map[string][]string `json:"duplicate_canonical_keys"`
	DuplicateFTSRows       [][]any             `json:"duplicate_fts_rows"`
	OrphanFTSRows          int                 `json:"orphan_fts_rows"`
}

type searchResult struct {
	Title      string  `json:"title"`
	ItemType   string  `json:"item_type"`
	Status     string  `json:"status"`
	Project    *string `json:"project"`
	SourcePath *string `json:"source_path"`
	Excerpt    *string `json:"excerpt"`
	Rank       float64 `json:"rank"`
}

type queryer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func utcNow() string {
	return time.Now().UTC().Truncate(time.Second).Format(time.RFC3339)
}

func discoverPaths() (paths, error) {
	executable, err := os.Executable()
	if err != nil {
		return paths{}, fmt.Errorf("locate executable: %w", err)
	}
	if resolved, resolveErr := filepath.EvalSymlinks(executable); resolveErr == nil {
		executable = resolved
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return paths{}, fmt.Errorf("resolve executable: %w", err)
	}

	root := strings.TrimSpace(os.Getenv("SECOND_BRAIN_ROOT"))
	executableDirectory := filepath.Dir(executable)
	executableDirectoryName := filepath.Base(executableDirectory)
	if root == "" && executableDirectoryName == programDirectoryName {
		root = filepath.Dir(executableDirectory)
	}
	if root == "" {
		workingDirectory, cwdErr := os.Getwd()
		if cwdErr != nil {
			return paths{}, fmt.Errorf("get working directory: %w", cwdErr)
		}
		root = findVaultRoot(workingDirectory)
	}
	if root == "" {
		return paths{}, errors.New("cannot locate SECOND_BRAIN; run the binary from the vault or set SECOND_BRAIN_ROOT")
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return paths{}, fmt.Errorf("resolve SECOND_BRAIN root: %w", err)
	}

	database := strings.TrimSpace(os.Getenv("SECOND_BRAIN_DB_PATH"))
	if database == "" {
		database = filepath.Join(root, "db.sqlite")
	} else if database, err = filepath.Abs(database); err != nil {
		return paths{}, fmt.Errorf("resolve database path: %w", err)
	}

	programDirectory := filepath.Join(root, programDirectoryName)
	schemaFile := filepath.Join(programDirectory, schemaDirectoryName, schemaFileName)
	errorLogFile := filepath.Join(programDirectory, logDirectoryName, errorLogFileName)
	return paths{
		executable: executable,
		vaultRoot:  root,
		database:   database,
		schema:     schemaFile,
		dirty:      filepath.Join(programDirectory, ".index-dirty"),
		errorLog:   errorLogFile,
	}, nil
}

func findVaultRoot(start string) string {
	current, err := filepath.Abs(start)
	if err != nil {
		return ""
	}
	for {
		marker := filepath.Join(current, programDirectoryName, schemaDirectoryName, schemaFileName)
		if fileExists(marker) {
			return current
		}
		parent := filepath.Dir(current)
		if parent == current {
			return ""
		}
		current = parent
	}
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func parseScalar(raw string) any {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ""
	}
	switch strings.ToLower(value) {
	case "true", "yes", "on":
		return true
	case "false", "no", "off":
		return false
	case "null", "~":
		return nil
	}
	if len(value) >= 2 && value[0] == '\'' && value[len(value)-1] == '\'' {
		return strings.ReplaceAll(value[1:len(value)-1], "''", "'")
	}
	if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
		if decoded, err := strconv.Unquote(value); err == nil {
			return decoded
		}
		return value[1 : len(value)-1]
	}
	if strings.HasPrefix(value, "[") && strings.HasSuffix(value, "]") {
		return parseInlineList(value[1 : len(value)-1])
	}
	return value
}

func parseInlineList(value string) []any {
	parts := make([]string, 0)
	var current strings.Builder
	var quote rune
	escaped := false
	for _, character := range value {
		if escaped {
			current.WriteRune(character)
			escaped = false
			continue
		}
		if character == '\\' && quote != 0 {
			current.WriteRune(character)
			escaped = true
			continue
		}
		if character == '\'' || character == '"' {
			if quote == 0 {
				quote = character
			} else if quote == character {
				quote = 0
			}
			current.WriteRune(character)
			continue
		}
		if character == ',' && quote == 0 {
			parts = append(parts, current.String())
			current.Reset()
			continue
		}
		current.WriteRune(character)
	}
	parts = append(parts, current.String())
	result := make([]any, 0, len(parts))
	for _, part := range parts {
		if strings.TrimSpace(part) != "" {
			result = append(result, parseScalar(part))
		}
	}
	return result
}

func parseFrontmatter(text string) (metadata, string) {
	result := newMetadata()
	openingLength := 0
	if strings.HasPrefix(text, "---\n") {
		openingLength = 4
	} else if strings.HasPrefix(text, "---\r\n") {
		openingLength = 5
	} else {
		return result, text
	}
	remainder := text[openingLength:]
	closing := closingRE.FindStringIndex(remainder)
	if closing == nil {
		return result, text
	}
	frontmatter := remainder[:closing[0]]
	body := remainder[closing[1]:]

	currentKey := ""
	blockStyle := ""
	blockLines := make([]string, 0)
	finishBlock := func() {
		if currentKey != "" && blockStyle != "" {
			separator := "\n"
			if strings.HasPrefix(blockStyle, ">") {
				separator = " "
			}
			result.values[currentKey] = strings.TrimSpace(strings.Join(blockLines, separator))
		}
		blockStyle = ""
		blockLines = blockLines[:0]
	}

	scanner := bufio.NewScanner(strings.NewReader(frontmatter))
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		match := topLevelRE.FindStringSubmatch(line)
		if match != nil {
			finishBlock()
			currentKey = match[1]
			rawValue := strings.TrimSpace(match[2])
			switch rawValue {
			case "|", "|-", "|+", ">", ">-", ">+":
				blockStyle = rawValue
				result.set(currentKey, "")
			case "":
				result.set(currentKey, []any{})
			default:
				result.set(currentKey, parseScalar(rawValue))
			}
			continue
		}
		if currentKey == "" {
			continue
		}
		stripped := strings.TrimSpace(line)
		if blockStyle != "" && (strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") || stripped == "") {
			blockLines = append(blockLines, stripped)
			continue
		}
		if strings.HasPrefix(stripped, "- ") {
			list, ok := result.values[currentKey].([]any)
			if !ok {
				list = []any{}
			}
			result.values[currentKey] = append(list, parseScalar(stripped[2:]))
		}
	}
	finishBlock()
	return result, body
}

func yamlScalar(value any) string {
	switch typed := value.(type) {
	case bool:
		if typed {
			return "true"
		}
		return "false"
	case nil:
		return "null"
	default:
		encoded, _ := marshalJSONNoHTML(fmt.Sprint(typed))
		return string(encoded)
	}
}

func serializeFrontmatter(meta metadata, body string) (string, error) {
	var output strings.Builder
	output.WriteString("---\n")
	for _, key := range meta.order {
		value := meta.values[key]
		switch typed := value.(type) {
		case []any:
			output.WriteString(key + ":\n")
			for _, item := range typed {
				output.WriteString("  - " + yamlScalar(item) + "\n")
			}
		case []string:
			output.WriteString(key + ":\n")
			for _, item := range typed {
				output.WriteString("  - " + yamlScalar(item) + "\n")
			}
		case map[string]any:
			return "", fmt.Errorf("cannot serialize nested clipping property %q", key)
		default:
			output.WriteString(key + ": " + yamlScalar(value) + "\n")
		}
	}
	output.WriteString("---\n")
	output.WriteString(body)
	return output.String(), nil
}

func clippingDescription(body, title string) string {
	paragraph := make([]string, 0)
	inCode := false
	for _, line := range strings.Split(body, "\n") {
		stripped := strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if strings.HasPrefix(stripped, "```") {
			inCode = !inCode
			continue
		}
		if inCode {
			continue
		}
		if stripped == "" {
			if len(paragraph) > 0 {
				break
			}
			continue
		}
		if strings.HasPrefix(stripped, "#") || strings.HasPrefix(stripped, "- ") ||
			strings.HasPrefix(stripped, "* ") || strings.HasPrefix(stripped, ">") ||
			strings.HasPrefix(stripped, "|") || strings.HasPrefix(stripped, "![[") ||
			strings.HasPrefix(stripped, "![](") {
			continue
		}
		paragraph = append(paragraph, stripped)
		if len(strings.Join(paragraph, " ")) >= 320 {
			break
		}
	}
	description := strings.TrimSpace(strings.Join(paragraph, " "))
	if description == "" {
		description = "Clipping Web sur " + title + "."
	}
	runes := []rune(description)
	if len(runes) > 320 {
		description = string(runes[:320])
	}
	return description
}

func harmonizeClipping(path, relative string, original metadata, body string) (metadata, error) {
	title := asText(original.values["title"])
	if title == "" {
		for _, line := range strings.Split(body, "\n") {
			if strings.HasPrefix(line, "# ") {
				title = strings.TrimSpace(strings.TrimPrefix(line, "# "))
				break
			}
		}
		if title == "" {
			title = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
		}
	}
	description := asText(original.values["description"])
	if description == "" {
		description = clippingDescription(body, title)
	}
	tags := asTags(original.values["tags"])
	for _, tag := range []string{"clippings", "inbox", "reference"} {
		if !contains(tags, tag) {
			tags = append(tags, tag)
		}
	}
	canonicalKey := asText(original.values["canonical_key"])
	if canonicalKey == "" {
		if source := asText(original.values["source"]); source != "" {
			canonicalKey = "source:" + source
		} else {
			canonicalKey = "file:" + strings.TrimSuffix(relative, ".md")
		}
	}

	normalized := newMetadata()
	normalized.set("title", title)
	normalized.set("description", description)
	normalized.set("type", "reference")
	normalized.set("status", "draft")
	normalized.set("index", true)
	normalized.set("canonical_key", canonicalKey)
	normalized.set("tags", tags)
	normalized.set("subtype", "web_clipping")
	required := make(map[string]bool, len(requiredKeys))
	for _, key := range requiredKeys {
		required[key] = true
	}
	for _, key := range original.order {
		if !required[key] && key != "area" && key != "processing" {
			normalized.set(key, original.values[key])
		}
	}
	normalized.set("area", "inbox")
	normalized.set("processing", "inbox")

	serialized, err := serializeFrontmatter(normalized, body)
	if err != nil {
		return metadata{}, err
	}
	if err := os.WriteFile(path, []byte(serialized), 0o644); err != nil {
		return metadata{}, fmt.Errorf("harmonize %s: %w", relative, err)
	}
	return normalized, nil
}

func asText(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	case float64:
		return strconv.FormatFloat(typed, 'g', -1, 64)
	default:
		return ""
	}
}

func asBool(value any, defaultValue bool) bool {
	if value == nil {
		return defaultValue
	}
	if typed, ok := value.(bool); ok {
		return typed
	}
	switch strings.ToLower(strings.TrimSpace(fmt.Sprint(value))) {
	case "false", "no", "off", "0":
		return false
	default:
		return true
	}
}

func asTags(value any) []string {
	values := make([]string, 0)
	switch typed := value.(type) {
	case []any:
		for _, item := range typed {
			values = append(values, fmt.Sprint(item))
		}
	case []string:
		values = append(values, typed...)
	case string:
		values = append(values, strings.Split(typed, ",")...)
	}
	cleaned := make([]string, 0, len(values))
	seen := make(map[string]bool)
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" && !seen[value] {
			seen[value] = true
			cleaned = append(cleaned, value)
		}
	}
	return cleaned
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func marshalJSONNoHTML(value any) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buffer.Bytes(), []byte("\n")), nil
}

func semanticHash(fields map[string]any) (string, error) {
	payload, err := marshalJSONNoHTML(fields)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:]), nil
}

func documentPaths(root string) ([]string, error) {
	result := make([]string, 0)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path != root && entry.IsDir() && strings.HasPrefix(entry.Name(), ".") {
			return filepath.SkipDir
		}
		extension := strings.ToLower(filepath.Ext(entry.Name()))
		isDocument := extension == ".md" || extension == ".html"
		if entry.IsDir() || entry.Name() == "AGENTS.md" || !isDocument {
			return nil
		}
		result = append(result, path)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(result)
	return result, nil
}

func htmlAttribute(tag, wanted string) string {
	matches := htmlAttributeRE.FindAllStringSubmatch(tag, -1)
	for _, match := range matches {
		name := strings.ToLower(match[1])
		if name != wanted {
			continue
		}
		for _, value := range match[2:] {
			if value != "" {
				return html.UnescapeString(value)
			}
		}
	}
	return ""
}

func htmlMetadata(content string) (string, string) {
	title := ""
	titleMatch := htmlTitleRE.FindStringSubmatch(content)
	if len(titleMatch) > 1 {
		title = strings.TrimSpace(html.UnescapeString(htmlTagRE.ReplaceAllString(titleMatch[1], " ")))
	}
	description := ""
	metaTags := htmlMetaRE.FindAllString(content, -1)
	for _, tag := range metaTags {
		name := strings.ToLower(htmlAttribute(tag, "name"))
		if name != "description" {
			continue
		}
		description = strings.TrimSpace(htmlAttribute(tag, "content"))
		break
	}
	return title, description
}

func htmlVisibleText(content string) string {
	withoutComments := htmlCommentRE.ReplaceAllString(content, " ")
	withoutHidden := htmlHiddenRE.ReplaceAllString(withoutComments, " ")
	withBreaks := htmlBlockRE.ReplaceAllString(withoutHidden, "\n")
	withoutTags := htmlTagRE.ReplaceAllString(withBreaks, " ")
	decoded := html.UnescapeString(withoutTags)
	lines := strings.Split(decoded, "\n")
	visible := make([]string, 0, len(lines))
	for _, line := range lines {
		normalized := strings.Join(strings.Fields(line), " ")
		if normalized != "" {
			visible = append(visible, normalized)
		}
	}
	return strings.Join(visible, "\n")
}

func htmlDescription(body, title string) string {
	for _, line := range strings.Split(body, "\n") {
		if line == title {
			continue
		}
		runes := []rune(line)
		if len(runes) > 320 {
			return string(runes[:320])
		}
		if line != "" {
			return line
		}
	}
	return "Document HTML : " + title + "."
}

func inferredProject(relative string) string {
	parts := strings.Split(relative, "/")
	if len(parts) < 2 || parts[0] != "projects" {
		return ""
	}
	return parts[1]
}

func readHTML(path string, locations paths) (note, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return note{}, err
	}
	relative, err := filepath.Rel(locations.vaultRoot, path)
	if err != nil {
		return note{}, err
	}
	relative = filepath.ToSlash(relative)
	rawContent := string(bytes.TrimPrefix(content, []byte{0xef, 0xbb, 0xbf}))
	title, description := htmlMetadata(rawContent)
	body := htmlVisibleText(rawContent)
	if title == "" {
		lines := strings.Split(body, "\n")
		if len(lines) > 0 {
			title = lines[0]
		}
	}
	if title == "" {
		title = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}
	if description == "" {
		description = htmlDescription(body, title)
	}
	project := inferredProject(relative)
	hashFields := map[string]any{
		"title": title, "description": description, "type": "reference",
		"subtype": "html_document", "project": nullIfEmpty(project),
		"area": "project", "source_url": nil,
		"tags": []string{"html", "document"}, "body": body,
	}
	hash, err := semanticHash(hashFields)
	if err != nil {
		return note{}, fmt.Errorf("hash %s: %w", relative, err)
	}
	return note{
		path: relative, title: title, description: description, itemType: "reference",
		itemSubtype: "html_document", project: project, area: "project", status: "active",
		canonicalKey: "file:" + relative, tags: []string{"html", "document"}, sourceTool: "html",
		body: body, sourceHash: hash, index: true,
	}, nil
}

func readNote(path string, locations paths, harmonizeClippings bool) (note, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return note{}, err
	}
	content = bytes.TrimPrefix(content, []byte{0xef, 0xbb, 0xbf})
	meta, body := parseFrontmatter(string(content))
	relative, err := filepath.Rel(locations.vaultRoot, path)
	if err != nil {
		return note{}, err
	}
	relative = filepath.ToSlash(relative)
	missing := missingKeys(meta)
	if len(missing) > 0 && strings.HasPrefix(relative, "Clippings/") && harmonizeClippings {
		meta, err = harmonizeClipping(path, relative, meta, body)
		if err != nil {
			return note{}, err
		}
		missing = nil
	}
	if len(missing) > 0 {
		return note{}, fmt.Errorf("%s: missing frontmatter keys: %s", relative, strings.Join(missing, ", "))
	}

	title := asText(meta.values["title"])
	description := asText(meta.values["description"])
	itemType := asText(meta.values["type"])
	status := asText(meta.values["status"])
	canonicalKey := asText(meta.values["canonical_key"])
	if title == "" || description == "" || itemType == "" || status == "" || canonicalKey == "" {
		return note{}, fmt.Errorf("%s: required frontmatter values must not be empty", relative)
	}
	if !allowedStatuses[status] {
		return note{}, fmt.Errorf("%s: unsupported status %q", relative, status)
	}
	tags := asTags(meta.values["tags"])
	if len(tags) == 0 {
		return note{}, fmt.Errorf("%s: tags must contain at least one value", relative)
	}
	sourceURL := asText(meta.values["source"])
	if sourceURL == "" {
		sourceURL = asText(meta.values["source_url"])
	}
	sourceTool := asText(meta.values["source_tool"])
	if sourceTool == "" {
		if strings.HasPrefix(relative, "Clippings/") {
			sourceTool = "obsidian-web-clipper"
		} else {
			sourceTool = "markdown"
		}
	}
	itemSubtype := asText(meta.values["subtype"])
	if itemSubtype == "" {
		itemSubtype = asText(meta.values["role"])
	}
	project := asText(meta.values["project"])
	area := asText(meta.values["area"])
	hash, err := semanticHash(map[string]any{
		"title": title, "description": description, "type": itemType,
		"subtype": nullIfEmpty(itemSubtype), "project": nullIfEmpty(project),
		"area": nullIfEmpty(area), "source_url": nullIfEmpty(sourceURL),
		"tags": tags, "body": body,
	})
	if err != nil {
		return note{}, fmt.Errorf("hash %s: %w", relative, err)
	}
	return note{
		path: relative, title: title, description: description, itemType: itemType,
		itemSubtype: itemSubtype, project: project, area: area, status: status,
		priority: asText(meta.values["priority"]), canonicalKey: canonicalKey, tags: tags,
		sourceTool: sourceTool, sourceURL: sourceURL, repoPath: asText(meta.values["repo_path"]),
		body: body, sourceHash: hash, index: asBool(meta.values["index"], true), metadata: meta,
	}, nil
}

func readDocument(path string, locations paths, harmonizeClippings bool) (note, error) {
	extension := strings.ToLower(filepath.Ext(path))
	if extension == ".html" {
		return readHTML(path, locations)
	}
	return readNote(path, locations, harmonizeClippings)
}

func missingKeys(meta metadata) []string {
	missing := make([]string, 0)
	for _, key := range requiredKeys {
		if _, exists := meta.values[key]; !exists {
			missing = append(missing, key)
		}
	}
	return missing
}

func nullIfEmpty(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func sourcePathKey(path string) string {
	return strings.TrimSuffix(path, filepath.Ext(path))
}

func fileURI(path string, readOnly bool) string {
	cleaned := filepath.ToSlash(path)
	if len(cleaned) >= 2 && cleaned[1] == ':' {
		cleaned = "/" + cleaned
	}
	uri := (&url.URL{Scheme: "file", Path: cleaned}).String()
	if readOnly {
		uri += "?mode=ro"
	}
	return uri
}

func openConnection(ctx context.Context, locations paths, readOnly bool) (*connection, error) {
	db, err := sql.Open(driverName, fileURI(locations.database, readOnly))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	conn, err := db.Conn(ctx)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	result := &connection{db: db, conn: conn}
	if _, err = conn.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
		result.close()
		return nil, err
	}
	if _, err = conn.ExecContext(ctx, "PRAGMA busy_timeout = 30000"); err != nil {
		result.close()
		return nil, err
	}
	if !readOnly {
		for attempt := 0; attempt < 50; attempt++ {
			var journalMode string
			err = conn.QueryRowContext(ctx, "PRAGMA journal_mode = WAL").Scan(&journalMode)
			if err == nil {
				break
			}
			if !strings.Contains(strings.ToLower(err.Error()), "locked") || attempt == 49 {
				result.close()
				return nil, err
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	return result, nil
}

func ensureSchema(ctx context.Context, conn *sql.Conn, locations paths) error {
	if _, err := conn.ExecContext(ctx, "CREATE VIRTUAL TABLE temp.fts5_probe USING fts5(content)"); err != nil {
		return fmt.Errorf("SQLite FTS5 is required by the sync-index binary: %w", err)
	}
	if _, err := conn.ExecContext(ctx, "DROP TABLE temp.fts5_probe"); err != nil {
		return err
	}
	var exists int
	err := conn.QueryRowContext(ctx,
		"SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = 'memory_items'",
	).Scan(&exists)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil {
		columns := make(map[string]bool)
		rows, queryErr := conn.QueryContext(ctx, "PRAGMA table_info(memory_items)")
		if queryErr != nil {
			return queryErr
		}
		for rows.Next() {
			var cid int
			var name, columnType string
			var notNull, primaryKey int
			var defaultValue any
			if scanErr := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); scanErr != nil {
				_ = rows.Close()
				return scanErr
			}
			columns[name] = true
		}
		if rowsErr := rows.Close(); rowsErr != nil {
			return rowsErr
		}
		for _, column := range []string{"source_hash", "indexed_at"} {
			if columns[column] {
				continue
			}
			if _, alterErr := conn.ExecContext(ctx, "ALTER TABLE memory_items ADD COLUMN "+column+" TEXT"); alterErr != nil && !strings.Contains(strings.ToLower(alterErr.Error()), "duplicate column name") {
				return alterErr
			}
		}
	}
	schema, err := os.ReadFile(locations.schema)
	if err != nil {
		return fmt.Errorf("read schema: %w", err)
	}
	if _, err = conn.ExecContext(ctx, string(schema)); err != nil {
		return fmt.Errorf("apply schema: %w", err)
	}
	_, err = conn.ExecContext(ctx, "CREATE INDEX IF NOT EXISTS idx_items_source_hash ON memory_items(source_hash)")
	return err
}

func queryExisting(ctx context.Context, database queryer, clause, value string) (*existingRow, error) {
	row := database.QueryRowContext(ctx,
		"SELECT id, source_hash, source_path, status, canonical_key, body_md, created_at FROM memory_items WHERE "+clause+" = ?",
		value,
	)
	var result existingRow
	var sourceHash, sourcePath, canonicalKey, body sql.NullString
	if err := row.Scan(&result.id, &sourceHash, &sourcePath, &result.status, &canonicalKey, &body, &result.createdAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	result.sourceHash = sourceHash.String
	result.sourcePath = sourcePath.String
	result.canonicalKey = canonicalKey.String
	result.body = body.String
	return &result, nil
}

func existingForNote(ctx context.Context, database queryer, current note) (*existingRow, error) {
	byKey, err := queryExisting(ctx, database, "canonical_key", current.canonicalKey)
	if err != nil {
		return nil, err
	}
	byPath, err := queryExisting(ctx, database, "source_path", current.path)
	if err != nil {
		return nil, err
	}
	if byKey != nil && byPath != nil && byKey.id != byPath.id {
		return nil, fmt.Errorf("%s: canonical_key belongs to %s, but source_path belongs to %s", current.path, byKey.sourcePath, byPath.canonicalKey)
	}
	if byKey != nil {
		return byKey, nil
	}
	return byPath, nil
}

func obsoleteStatements(ctx context.Context, database queryer, itemID, timestamp string) (int, error) {
	var count int
	if err := database.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM memory_statements WHERE item_id = ? AND status != 'obsolete'", itemID,
	).Scan(&count); err != nil {
		return 0, err
	}
	if count == 0 {
		return 0, nil
	}
	if _, err := database.ExecContext(ctx,
		"UPDATE memory_statements SET status = 'obsolete', updated_at = ? WHERE item_id = ? AND status != 'obsolete'",
		timestamp, itemID,
	); err != nil {
		return 0, err
	}
	if _, err := database.ExecContext(ctx, "DELETE FROM memory_statements_fts WHERE item_id = ?", itemID); err != nil {
		return 0, err
	}
	return count, nil
}

func replaceTags(ctx context.Context, database queryer, itemID string, tags []string, timestamp string) error {
	if _, err := database.ExecContext(ctx, "DELETE FROM memory_item_tags WHERE item_id = ?", itemID); err != nil {
		return err
	}
	for _, tag := range tags {
		tagID := uuidV5("tag:" + tag)
		if _, err := database.ExecContext(ctx,
			"INSERT INTO memory_tags (id, name, created_at) VALUES (?, ?, ?) ON CONFLICT(name) DO UPDATE SET name = excluded.name",
			tagID, tag, timestamp,
		); err != nil {
			return err
		}
		var storedTagID string
		if err := database.QueryRowContext(ctx, "SELECT id FROM memory_tags WHERE name = ?", tag).Scan(&storedTagID); err != nil {
			return err
		}
		if _, err := database.ExecContext(ctx,
			"INSERT OR IGNORE INTO memory_item_tags (item_id, tag_id, created_at) VALUES (?, ?, ?)",
			itemID, storedTagID, timestamp,
		); err != nil {
			return err
		}
	}
	return nil
}

func replaceItemFTS(ctx context.Context, database queryer, itemID string, current note) error {
	if _, err := database.ExecContext(ctx, "DELETE FROM memory_items_fts WHERE item_id = ?", itemID); err != nil {
		return err
	}
	_, err := database.ExecContext(ctx,
		"INSERT INTO memory_items_fts (item_id, item_type, item_subtype, project, area, title, summary, body_md, tags) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
		itemID, current.itemType, nullIfEmpty(current.itemSubtype), nullIfEmpty(current.project),
		nullIfEmpty(current.area), current.title, current.description, current.body, strings.Join(current.tags, " "),
	)
	return err
}

func searchFallbackQuery(query string) string {
	terms := strings.FieldsFunc(query, func(character rune) bool {
		return !unicode.IsLetter(character) && !unicode.IsNumber(character)
	})
	if len(terms) < 2 {
		return ""
	}
	return strings.Join(terms, " OR ")
}

func replaceWikiLinkRelations(ctx context.Context, database queryer, notes []note, timestamp string) (int, error) {
	if _, err := database.ExecContext(ctx, "DELETE FROM memory_item_relations WHERE relation = 'wikilink'"); err != nil {
		return 0, err
	}
	itemIDsByPath := make(map[string]string)
	rows, err := database.QueryContext(ctx, "SELECT id, source_path FROM memory_items WHERE status != 'obsolete' AND source_path IS NOT NULL")
	if err != nil {
		return 0, err
	}
	for rows.Next() {
		var itemID, sourcePath string
		if err = rows.Scan(&itemID, &sourcePath); err != nil {
			_ = rows.Close()
			return 0, err
		}
		itemIDsByPath[sourcePathKey(sourcePath)] = itemID
	}
	if err = rows.Close(); err != nil {
		return 0, err
	}
	if err = rows.Err(); err != nil {
		return 0, err
	}
	seen := make(map[string]bool)
	count := 0
	for _, current := range notes {
		if !current.index {
			continue
		}
		fromID, exists := itemIDsByPath[sourcePathKey(current.path)]
		if !exists {
			continue
		}
		for _, match := range wikiLinkRE.FindAllStringSubmatch(current.body, -1) {
			targetPath := sourcePathKey(strings.TrimSpace(match[1]))
			toID, exists := itemIDsByPath[targetPath]
			if !exists || toID == fromID {
				continue
			}
			key := fromID + ":" + toID
			if seen[key] {
				continue
			}
			seen[key] = true
			if _, err := database.ExecContext(ctx,
				"INSERT INTO memory_item_relations (id, from_item_id, relation, to_item_id, relation_note, confidence_score, created_at) VALUES (?, ?, 'wikilink', ?, ?, ?, ?)",
				uuidV5("relation:wikilink:"+key), fromID, toID, targetPath, 10, timestamp,
			); err != nil {
				return count, err
			}
			count++
		}
	}
	return count, nil
}

func upsertNote(ctx context.Context, database queryer, current note, timestamp string, force bool) (string, int, error) {
	existing, err := existingForNote(ctx, database, current)
	if err != nil {
		return "", 0, err
	}
	unchanged := existing != nil && existing.sourceHash == current.sourceHash &&
		existing.sourcePath == current.path && existing.status == current.status &&
		existing.canonicalKey == current.canonicalKey
	if unchanged && !force {
		return "unchanged", 0, nil
	}

	itemID := uuidV5(current.canonicalKey)
	statementsObsoleted := 0
	createdAt := asText(current.metadata.values["created"])
	if existing != nil {
		itemID = existing.id
		createdAt = existing.createdAt
		if strings.TrimRightFunc(existing.body, unicode.IsSpace) != strings.TrimRightFunc(current.body, unicode.IsSpace) {
			statementsObsoleted, err = obsoleteStatements(ctx, database, itemID, timestamp)
			if err != nil {
				return "", 0, err
			}
		}
	}
	if createdAt == "" {
		createdAt = timestamp
	}
	var archivedAt any
	if current.status == "archived" || current.status == "obsolete" {
		archivedAt = timestamp
	}
	tagsJSON, err := marshalJSONNoHTML(current.tags)
	if err != nil {
		return "", 0, err
	}
	_, err = database.ExecContext(ctx, `
		INSERT INTO memory_items (
		  id, item_type, item_subtype, project, area, title, summary, body_md,
		  status, priority, canonical_key, source_tool, source_path, source_hash,
		  source_url, repo_path, tags_json, created_at, updated_at, indexed_at, archived_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
		  item_type = excluded.item_type,
		  item_subtype = excluded.item_subtype,
		  project = excluded.project,
		  area = excluded.area,
		  title = excluded.title,
		  summary = excluded.summary,
		  body_md = excluded.body_md,
		  status = excluded.status,
		  priority = excluded.priority,
		  canonical_key = excluded.canonical_key,
		  source_tool = excluded.source_tool,
		  source_path = excluded.source_path,
		  source_hash = excluded.source_hash,
		  source_url = excluded.source_url,
		  repo_path = excluded.repo_path,
		  tags_json = excluded.tags_json,
		  updated_at = excluded.updated_at,
		  indexed_at = excluded.indexed_at,
		  archived_at = excluded.archived_at`,
		itemID, current.itemType, nullIfEmpty(current.itemSubtype), nullIfEmpty(current.project),
		nullIfEmpty(current.area), current.title, current.description, current.body, current.status,
		nullIfEmpty(current.priority), current.canonicalKey, current.sourceTool, current.path,
		current.sourceHash, nullIfEmpty(current.sourceURL), nullIfEmpty(current.repoPath),
		string(tagsJSON), createdAt, timestamp, timestamp, archivedAt,
	)
	if err != nil {
		return "", 0, err
	}
	if err = replaceTags(ctx, database, itemID, current.tags, timestamp); err != nil {
		return "", 0, err
	}
	if err = replaceItemFTS(ctx, database, itemID, current); err != nil {
		return "", 0, err
	}
	if existing != nil {
		return "updated", statementsObsoleted, nil
	}
	return "inserted", statementsObsoleted, nil
}

func markItemObsolete(ctx context.Context, database queryer, itemID, timestamp string) (int, error) {
	if _, err := database.ExecContext(ctx,
		"UPDATE memory_items SET status = 'obsolete', updated_at = ?, indexed_at = ?, archived_at = COALESCE(archived_at, ?) WHERE id = ?",
		timestamp, timestamp, timestamp, itemID,
	); err != nil {
		return 0, err
	}
	if _, err := database.ExecContext(ctx, "DELETE FROM memory_items_fts WHERE item_id = ?", itemID); err != nil {
		return 0, err
	}
	return obsoleteStatements(ctx, database, itemID, timestamp)
}

func loadNotes(locations paths, harmonize bool) ([]note, error) {
	documents, err := documentPaths(locations.vaultRoot)
	if err != nil {
		return nil, err
	}
	notes := make([]note, 0, len(documents))
	for _, path := range documents {
		current, readErr := readDocument(path, locations, harmonize)
		if readErr != nil {
			return nil, readErr
		}
		notes = append(notes, current)
	}
	return notes, nil
}

func synchronize(ctx context.Context, locations paths, force, onlyIfDirty bool) (counts, error) {
	result := counts{}
	if onlyIfDirty && !fileExists(locations.dirty) {
		return result, nil
	}
	notes, err := loadNotes(locations, true)
	if err != nil {
		return result, err
	}
	canonicalKeys := make(map[string]string)
	for _, current := range notes {
		if previous := canonicalKeys[current.canonicalKey]; previous != "" {
			return result, fmt.Errorf("duplicate canonical_key %q: %s and %s", current.canonicalKey, previous, current.path)
		}
		canonicalKeys[current.canonicalKey] = current.path
	}
	presentPaths := make(map[string]bool)
	for _, current := range notes {
		presentPaths[current.path] = true
	}
	result.Scanned = len(notes)
	timestamp := utcNow()

	database, err := openConnection(ctx, locations, false)
	if err != nil {
		return result, err
	}
	defer database.close()
	if err = ensureSchema(ctx, database.conn, locations); err != nil {
		return result, err
	}
	if _, err = database.conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return result, err
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = database.conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()

	for _, current := range notes {
		if current.index {
			action, statements, upsertErr := upsertNote(ctx, database.conn, current, timestamp, force)
			if upsertErr != nil {
				return result, upsertErr
			}
			switch action {
			case "inserted":
				result.Inserted++
			case "updated":
				result.Updated++
			case "unchanged":
				result.Unchanged++
			}
			result.StatementsObsoleted += statements
			continue
		}
		existing, existingErr := existingForNote(ctx, database.conn, current)
		if existingErr != nil {
			return result, existingErr
		}
		if existing != nil && existing.status != "obsolete" {
			statements, obsoleteErr := markItemObsolete(ctx, database.conn, existing.id, timestamp)
			if obsoleteErr != nil {
				return result, obsoleteErr
			}
			result.StatementsObsoleted += statements
			result.Obsoleted++
		}
	}

	type managedRow struct{ id, path, status string }
	managed := make([]managedRow, 0)
	rows, err := database.conn.QueryContext(ctx,
		"SELECT id, source_path, status FROM memory_items WHERE source_path IS NOT NULL AND (source_path LIKE '%.md' OR source_path LIKE '%.html')",
	)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var row managedRow
		if err = rows.Scan(&row.id, &row.path, &row.status); err != nil {
			_ = rows.Close()
			return result, err
		}
		managed = append(managed, row)
	}
	if err = rows.Close(); err != nil {
		return result, err
	}
	for _, row := range managed {
		if presentPaths[row.path] || row.status == "obsolete" {
			continue
		}
		statements, obsoleteErr := markItemObsolete(ctx, database.conn, row.id, timestamp)
		if obsoleteErr != nil {
			return result, obsoleteErr
		}
		result.StatementsObsoleted += statements
		result.Obsoleted++
	}
	if _, err = replaceWikiLinkRelations(ctx, database.conn, notes, timestamp); err != nil {
		return result, err
	}
	if _, err = database.conn.ExecContext(ctx, "COMMIT"); err != nil {
		return result, err
	}
	committed = true
	if err = os.Remove(locations.dirty); err != nil && !errors.Is(err, os.ErrNotExist) {
		return result, err
	}
	return result, nil
}

func checkIndex(ctx context.Context, locations paths) (checkResult, error) {
	result := checkResult{
		MissingRows: []string{}, MismatchedRows: []string{},
		DuplicateCanonicalKeys: map[string][]string{}, DuplicateFTSRows: [][]any{},
	}
	notes, err := loadNotes(locations, false)
	if err != nil {
		return result, err
	}
	indexed := make([]note, 0)
	activeIndexedCount := 0
	byKey := make(map[string][]string)
	for _, current := range notes {
		if current.index {
			indexed = append(indexed, current)
			if current.status != "obsolete" {
				activeIndexedCount++
			}
			byKey[current.canonicalKey] = append(byKey[current.canonicalKey], current.path)
		}
	}
	for key, notePaths := range byKey {
		if len(notePaths) > 1 {
			result.DuplicateCanonicalKeys[key] = notePaths
		}
	}
	database, err := openConnection(ctx, locations, true)
	if err != nil {
		return result, err
	}
	defer database.close()
	if err = database.conn.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&result.Integrity); err != nil {
		return result, err
	}
	foreignRows, err := database.conn.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return result, err
	}
	for foreignRows.Next() {
		result.ForeignKeyErrors++
	}
	if err = foreignRows.Close(); err != nil {
		return result, err
	}
	for _, current := range indexed {
		var sourcePath, status string
		var sourceHash sql.NullString
		err = database.conn.QueryRowContext(ctx,
			"SELECT source_path, status, source_hash FROM memory_items WHERE canonical_key = ?",
			current.canonicalKey,
		).Scan(&sourcePath, &status, &sourceHash)
		if errors.Is(err, sql.ErrNoRows) {
			result.MissingRows = append(result.MissingRows, current.path)
			continue
		}
		if err != nil {
			return result, err
		}
		if sourcePath != current.path || status != current.status {
			result.MismatchedRows = append(result.MismatchedRows, current.path)
		}
	}
	duplicateRows, err := database.conn.QueryContext(ctx,
		"SELECT item_id, COUNT(*) FROM memory_items_fts GROUP BY item_id HAVING COUNT(*) > 1",
	)
	if err != nil {
		return result, err
	}
	for duplicateRows.Next() {
		var itemID string
		var count int
		if err = duplicateRows.Scan(&itemID, &count); err != nil {
			_ = duplicateRows.Close()
			return result, err
		}
		result.DuplicateFTSRows = append(result.DuplicateFTSRows, []any{itemID, count})
	}
	if err = duplicateRows.Close(); err != nil {
		return result, err
	}
	if err = database.conn.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM memory_items_fts f LEFT JOIN memory_items i ON i.id = f.item_id WHERE i.id IS NULL",
	).Scan(&result.OrphanFTSRows); err != nil {
		return result, err
	}
	if err = database.conn.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM memory_items WHERE (source_path LIKE '%.md' OR source_path LIKE '%.html') AND status != 'obsolete'",
	).Scan(&result.ActiveSourceRows); err != nil {
		return result, err
	}
	result.SourceFiles = len(notes)
	result.IndexableNotes = len(indexed)
	valid := result.Integrity == "ok" && result.ForeignKeyErrors == 0 &&
		len(result.MissingRows) == 0 && len(result.MismatchedRows) == 0 &&
		len(result.DuplicateCanonicalKeys) == 0 && len(result.DuplicateFTSRows) == 0 &&
		result.OrphanFTSRows == 0 && result.ActiveSourceRows == activeIndexedCount
	if !valid {
		encoded, _ := json.MarshalIndent(result, "", "  ")
		return result, errors.New(string(encoded))
	}
	return result, nil
}

func searchIndex(ctx context.Context, locations paths, query string, includeArchived bool, limit int) ([]searchResult, error) {
	statuses := []string{"active", "draft"}
	if includeArchived {
		statuses = append(statuses, "archived")
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(statuses)), ",")
	statement := `
		SELECT i.title, i.item_type, i.status, i.project, i.source_path, i.source_hash,
		       snippet(memory_items_fts, 7, '[', ']', ' … ', 18) AS excerpt,
		       bm25(memory_items_fts) AS rank
		FROM memory_items_fts
		JOIN memory_items i ON i.id = memory_items_fts.item_id
		WHERE memory_items_fts MATCH ? AND i.status IN (` + placeholders + `)
		ORDER BY rank
		LIMIT ?`
	fetchLimit := limit * searchDeduplicationFetchMultiplier
	arguments := make([]any, 0, len(statuses)+2)
	arguments = append(arguments, query)
	for _, status := range statuses {
		arguments = append(arguments, status)
	}
	arguments = append(arguments, fetchLimit)
	seenContent := make(map[string]bool)
	database, err := openConnection(ctx, locations, true)
	if err != nil {
		return nil, err
	}
	defer database.close()
	rows, err := database.conn.QueryContext(ctx, statement, arguments...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	results := make([]searchResult, 0)
	for rows.Next() {
		var result searchResult
		var project, sourcePath, sourceHash, excerpt sql.NullString
		if err = rows.Scan(&result.Title, &result.ItemType, &result.Status, &project, &sourcePath, &sourceHash, &excerpt, &result.Rank); err != nil {
			return nil, err
		}
		result.Project = stringPointer(project)
		result.SourcePath = stringPointer(sourcePath)
		result.Excerpt = stringPointer(excerpt)
		contentKey := sourcePath.String
		if sourceHash.Valid {
			contentKey = sourceHash.String
		}
		if seenContent[contentKey] {
			continue
		}
		seenContent[contentKey] = true
		results = append(results, result)
		if len(results) >= limit {
			break
		}
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if len(results) > 0 {
		return results, nil
	}
	fallback := searchFallbackQuery(query)
	if fallback == "" {
		return results, nil
	}
	arguments[0] = fallback
	rows, err = database.conn.QueryContext(ctx, statement, arguments...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var result searchResult
		var project, sourcePath, sourceHash, excerpt sql.NullString
		if err = rows.Scan(&result.Title, &result.ItemType, &result.Status, &project, &sourcePath, &sourceHash, &excerpt, &result.Rank); err != nil {
			return nil, err
		}
		result.Project = stringPointer(project)
		result.SourcePath = stringPointer(sourcePath)
		result.Excerpt = stringPointer(excerpt)
		contentKey := sourcePath.String
		if sourceHash.Valid {
			contentKey = sourceHash.String
		}
		if seenContent[contentKey] {
			continue
		}
		seenContent[contentKey] = true
		results = append(results, result)
		if len(results) >= limit {
			break
		}
	}
	return results, rows.Err()
}

func stringPointer(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	return &value.String
}

func markDirty(locations paths) error {
	return os.WriteFile(locations.dirty, []byte(utcNow()+"\n"), 0o644)
}

func uuidV5(name string) string {
	namespace, _ := hex.DecodeString(strings.ReplaceAll(uuidNamespace, "-", ""))
	digest := sha1.Sum(append(namespace, []byte(name)...))
	value := digest[:16]
	value[6] = (value[6] & 0x0f) | 0x50
	value[8] = (value[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(value)
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:32]
}

func shellQuote(argument string) string {
	if argument != "" && strings.IndexFunc(argument, func(r rune) bool {
		return !(unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("_@%+=:,./-", r))
	}) == -1 {
		return argument
	}
	return "'" + strings.ReplaceAll(argument, "'", "'\"'\"'") + "'"
}

func windowsQuote(argument string) string {
	if argument != "" && !strings.ContainsAny(argument, " \t\"") {
		return argument
	}
	var result strings.Builder
	result.WriteByte('"')
	backslashes := 0
	for _, character := range argument {
		if character == '\\' {
			backslashes++
			continue
		}
		if character == '"' {
			result.WriteString(strings.Repeat("\\", backslashes*2+1))
			result.WriteRune(character)
			backslashes = 0
			continue
		}
		result.WriteString(strings.Repeat("\\", backslashes))
		backslashes = 0
		result.WriteRune(character)
	}
	result.WriteString(strings.Repeat("\\", backslashes*2))
	result.WriteByte('"')
	return result.String()
}

func hookCommand(mode, executable, platform string) string {
	arguments := []string{executable, "--hook", mode}
	quoted := make([]string, len(arguments))
	for index, argument := range arguments {
		if platform == "windows" {
			quoted[index] = windowsQuote(argument)
		} else {
			quoted[index] = shellQuote(argument)
		}
	}
	return strings.Join(quoted, " ")
}

func readJSONConfig(path string) (map[string]any, error) {
	content, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, err
	}
	content = bytes.TrimPrefix(content, []byte{0xef, 0xbb, 0xbf})
	var config map[string]any
	if err = json.Unmarshal(content, &config); err != nil {
		return nil, fmt.Errorf("%s: invalid JSON: %w", path, err)
	}
	if config == nil {
		return nil, fmt.Errorf("%s: root JSON value must be an object", path)
	}
	return config, nil
}

func isSyncIndexHook(value any) bool {
	hook, ok := value.(map[string]any)
	if !ok {
		return false
	}
	command := strings.ReplaceAll(fmt.Sprint(hook["command"]), "\\", "/")
	return strings.Contains(command, "ai-memory/sync-index")
}

func replaceSyncIndexHook(config map[string]any, event, mode string, matcher *string, timeout int, statusMessage, platform, executable string) error {
	hooksByEvent, exists := config["hooks"]
	if !exists {
		hooksByEvent = map[string]any{}
		config["hooks"] = hooksByEvent
	}
	hooksObject, ok := hooksByEvent.(map[string]any)
	if !ok {
		return errors.New("hooks must be a JSON object")
	}
	groups := []any{}
	if storedGroups, exists := hooksObject[event]; exists {
		var valid bool
		groups, valid = storedGroups.([]any)
		if !valid {
			return fmt.Errorf("hooks.%s must be a JSON array", event)
		}
	}
	preservedGroups := make([]any, 0, len(groups)+1)
	for _, rawGroup := range groups {
		group, valid := rawGroup.(map[string]any)
		if !valid {
			preservedGroups = append(preservedGroups, rawGroup)
			continue
		}
		commands, valid := group["hooks"].([]any)
		if !valid {
			preservedGroups = append(preservedGroups, rawGroup)
			continue
		}
		remaining := make([]any, 0, len(commands))
		for _, command := range commands {
			if !isSyncIndexHook(command) {
				remaining = append(remaining, command)
			}
		}
		if len(remaining) > 0 {
			preserved := make(map[string]any, len(group))
			for key, value := range group {
				preserved[key] = value
			}
			preserved["hooks"] = remaining
			preservedGroups = append(preservedGroups, preserved)
		}
	}
	commandHook := map[string]any{
		"type": "command", "command": hookCommand(mode, executable, platform),
		"timeout": timeout, "statusMessage": statusMessage,
	}
	newGroup := map[string]any{"hooks": []any{commandHook}}
	if matcher != nil && *matcher != "" {
		newGroup["matcher"] = *matcher
	}
	preservedGroups = append(preservedGroups, newGroup)
	hooksObject[event] = preservedGroups
	return nil
}

func writeJSONConfig(path string, config map[string]any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	content, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	content = append(content, '\n')
	temporary := filepath.Join(filepath.Dir(path), fmt.Sprintf(".%s.%d.tmp", filepath.Base(path), os.Getpid()))
	if err = os.WriteFile(temporary, content, 0o644); err != nil {
		return err
	}
	if err = os.Rename(temporary, path); err == nil {
		return nil
	}
	if runtime.GOOS != "windows" {
		_ = os.Remove(temporary)
		return err
	}
	if removeErr := os.Remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
		_ = os.Remove(temporary)
		return removeErr
	}
	if err = os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
	}
	return err
}

func installHooks(locations paths) (map[string]string, error) {
	if strings.Contains(filepath.ToSlash(locations.executable), "/go-build") {
		return nil, errors.New("--install-hooks must be run with the built ai-memory/sync-index binary, not with go run")
	}
	star := "*"
	claudePost := "Edit|Write|MultiEdit"
	codexPost := "Edit|Write|apply_patch"
	clients := []struct {
		name                string
		path                string
		postToolMatcher     *string
		sessionStartMatcher *string
	}{
		{"claude", filepath.Join(locations.vaultRoot, ".claude", "settings.local.json"), &claudePost, &star},
		{"codex", filepath.Join(locations.vaultRoot, ".codex", "hooks.json"), &codexPost, nil},
	}
	events := []struct {
		name, mode, statusMessage string
		timeout                   int
	}{
		{"SessionStart", "--changed", "Synchronisation du SECOND_BRAIN", 15},
		{"UserPromptSubmit", "--changed", "Vérification de l'index mémoire", 15},
		{"PostToolUse", "--mark-dirty", "Index mémoire marqué à synchroniser", 5},
		{"Stop", "--if-dirty", "Finalisation de l'index mémoire", 15},
	}
	installed := make(map[string]string)
	for _, client := range clients {
		config, err := readJSONConfig(client.path)
		if err != nil {
			return nil, err
		}
		for _, event := range events {
			var matcher *string
			switch event.name {
			case "PostToolUse":
				matcher = client.postToolMatcher
			case "SessionStart":
				matcher = client.sessionStartMatcher
			}
			if err = replaceSyncIndexHook(config, event.name, event.mode, matcher, event.timeout, event.statusMessage, runtime.GOOS, locations.executable); err != nil {
				return nil, err
			}
		}
		if err = writeJSONConfig(client.path, config); err != nil {
			return nil, err
		}
		installed[client.name] = client.path
	}
	return installed, nil
}

func parseOptions(arguments []string) (options, error) {
	result := options{}
	flags := flag.NewFlagSet("sync-index", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	flags.BoolVar(&result.changed, "changed", false, "synchronize changed notes")
	flags.BoolVar(&result.full, "full", false, "upsert and rebuild FTS for every note")
	flags.BoolVar(&result.ifDirty, "if-dirty", false, "synchronize only after a write hook")
	flags.BoolVar(&result.check, "check", false, "verify database integrity and coverage")
	flags.BoolVar(&result.markDirty, "mark-dirty", false, "mark the index stale")
	flags.BoolVar(&result.installHooks, "install-hooks", false, "install project hooks for Claude Code and Codex")
	flags.BoolVar(&result.includeArchived, "include-archived", false, "include archived search results")
	flags.IntVar(&result.limit, "limit", 10, "maximum search results")
	flags.BoolVar(&result.hook, "hook", false, "log errors without blocking the host")
	flags.Func("search", "search active and draft Markdown items", func(value string) error {
		result.search = value
		result.searchSet = true
		return nil
	})
	if err := flags.Parse(arguments); err != nil {
		return result, err
	}
	if flags.NArg() != 0 {
		return result, fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}
	modes := 0
	for _, enabled := range []bool{result.changed, result.full, result.ifDirty, result.check, result.markDirty, result.searchSet, result.installHooks} {
		if enabled {
			modes++
		}
	}
	if modes > 1 {
		return result, errors.New("choose only one of --changed, --full, --if-dirty, --check, --mark-dirty, --search, or --install-hooks")
	}
	return result, nil
}

func printJSON(value any) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func run(ctx context.Context, locations paths, arguments options) error {
	if arguments.markDirty {
		return markDirty(locations)
	}
	if arguments.installHooks {
		installed, err := installHooks(locations)
		if err != nil {
			return err
		}
		return printJSON(installed)
	}
	if arguments.check {
		result, err := checkIndex(ctx, locations)
		if err != nil {
			return err
		}
		return printJSON(result)
	}
	if arguments.searchSet {
		limit := arguments.limit
		if limit < 1 {
			limit = 1
		} else if limit > 100 {
			limit = 100
		}
		results, err := searchIndex(ctx, locations, arguments.search, arguments.includeArchived, limit)
		if err != nil {
			return err
		}
		return printJSON(results)
	}
	result, err := synchronize(ctx, locations, arguments.full, arguments.ifDirty)
	if err != nil {
		return err
	}
	if !arguments.hook {
		return printJSON(result)
	}
	return nil
}

func drainHookInput() {
	info, err := os.Stdin.Stat()
	if err == nil && info.Mode()&os.ModeCharDevice == 0 {
		_, _ = io.Copy(io.Discard, os.Stdin)
	}
}

func appendHookError(path string, err error) {
	if makeErr := os.MkdirAll(filepath.Dir(path), 0o755); makeErr != nil {
		return
	}
	log, openErr := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if openErr != nil {
		return
	}
	defer log.Close()
	_, _ = fmt.Fprintf(log, "[%s] %T: %v\n\n", utcNow(), err, err)
}

func main() {
	arguments, err := parseOptions(os.Args[1:])
	if err != nil {
		os.Exit(2)
	}
	if arguments.hook {
		drainHookInput()
	}
	locations, pathErr := discoverPaths()
	if pathErr != nil {
		if arguments.hook {
			return
		}
		fmt.Fprintln(os.Stderr, pathErr)
		os.Exit(1)
	}
	if err = run(context.Background(), locations, arguments); err != nil {
		if arguments.hook {
			appendHookError(locations.errorLog, err)
			return
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
