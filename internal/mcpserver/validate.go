package mcpserver

import (
	"net/url"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"mcp-knowledge-graph/internal/knowledge"
)

// canonicalUUID matches the 36 character hyphenated UUID form. uuid.Parse
// additionally accepts URN and braced forms, which are not part of the
// public contract.
var canonicalUUID = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// parseID validates and parses a UUID argument.
func parseID(field, value string) (uuid.UUID, error) {
	if !canonicalUUID.MatchString(value) {
		return uuid.Nil, invalidInput("%s must be a UUID in canonical form, got %q", field, truncateForMessage(value))
	}
	id, err := uuid.Parse(value)
	if err != nil || id == uuid.Nil {
		return uuid.Nil, invalidInput("%s must be a non-nil UUID", field)
	}
	return id, nil
}

// parseOptionalID parses an optional UUID argument; "" means absent.
func parseOptionalID(field, value string) (*uuid.UUID, error) {
	if value == "" {
		return nil, nil
	}
	id, err := parseID(field, value)
	if err != nil {
		return nil, err
	}
	return &id, nil
}

// checkText validates a required single-line or Markdown text: valid UTF-8,
// non-blank after trimming and at most maxBytes long. The value itself is
// never rewritten here.
func checkText(field, value string, maxBytes int) error {
	if !utf8.ValidString(value) {
		return invalidInput("%s must be valid UTF-8", field)
	}
	if strings.TrimSpace(value) == "" {
		return invalidInput("%s must not be blank", field)
	}
	if len(value) > maxBytes {
		return invalidInput("%s exceeds the limit of %d bytes (got %d)", field, maxBytes, len(value))
	}
	return nil
}

// checkVersion validates a required expected version.
func checkVersion(field string, v int64) error {
	if v <= 0 {
		return invalidInput("%s must be a positive integer", field)
	}
	return nil
}

// limitOrDefault applies the default for an omitted (zero) limit and
// rejects values outside 1..maxValue.
func limitOrDefault(field string, v, def, maxValue int) (int, error) {
	if v == 0 {
		return def, nil
	}
	if v < 1 || v > maxValue {
		return 0, invalidInput("%s must be between 1 and %d, got %d", field, maxValue, v)
	}
	return v, nil
}

// blockedSchemes can execute code or reference local resources.
var blockedSchemes = []string{"javascript", "vbscript", "data", "file", "blob", "filesystem", "about"}

// secretQueryParams are query parameters that typically carry credentials
// or presigned, expiring signatures.
var secretQueryParams = []string{
	"access_token", "api_key", "apikey", "api-key", "auth", "authorization", "client_secret",
	"code", "id_token", "key", "password", "passwd", "pwd", "refresh_token", "secret",
	"sessionid", "sig", "signature", "token",
	"x-amz-signature", "x-amz-credential", "x-amz-security-token",
	"x-goog-signature", "x-goog-credential",
}

// checkSourceURI enforces the source URI policy: an absolute URI with a
// scheme, no embedded credentials, no presigned or secret-bearing query
// parameters and no executable or local schemes. The URI is never fetched.
func checkSourceURI(value string, maxBytes int) error {
	if err := checkText("source_uri", value, maxBytes); err != nil {
		return err
	}
	if strings.TrimSpace(value) != value {
		return invalidInput("source_uri must not have leading or trailing whitespace")
	}
	if strings.ContainsFunc(value, func(r rune) bool { return r < 0x20 || r == 0x7f || r == ' ' }) {
		return invalidInput("source_uri must not contain whitespace or control characters")
	}
	u, err := url.Parse(value)
	if err != nil || u.Scheme == "" {
		return invalidInput("source_uri must be an absolute URI with a scheme, e.g. https://... or urn:...")
	}
	scheme := strings.ToLower(u.Scheme)
	if slices.Contains(blockedSchemes, scheme) {
		return invalidInput("source_uri scheme %q is not allowed", scheme)
	}
	if (scheme == "http" || scheme == "https") && u.Host == "" {
		return invalidInput("source_uri must contain a host")
	}
	if u.User != nil {
		return invalidInput("source_uri must not contain user credentials")
	}
	for name := range u.Query() {
		if slices.Contains(secretQueryParams, strings.ToLower(name)) {
			return invalidInput("source_uri must not contain secret-bearing or presigned query parameters (%q); use a permanent identifier", name)
		}
	}
	return nil
}

// sourceInput validates a source reference argument.
func (h *Handler) sourceInput(kind, uri string, title *string) (knowledge.SourceInput, error) {
	k := knowledge.SourceKind(kind)
	if !k.Valid() {
		return knowledge.SourceInput{}, invalidInput("kind must be %q or %q, got %q",
			knowledge.SourceKindConversation, knowledge.SourceKindOther, truncateForMessage(kind))
	}
	if err := checkSourceURI(uri, h.limits.MaxSourceURIBytes); err != nil {
		return knowledge.SourceInput{}, err
	}
	if title != nil {
		if err := checkText("source title", *title, h.limits.MaxSourceTitleBytes); err != nil {
			return knowledge.SourceInput{}, err
		}
	}
	return knowledge.SourceInput{Kind: k, URI: uri, Title: title}, nil
}

// truncateForMessage shortens echoed user input in error messages.
func truncateForMessage(s string) string {
	const maxRunes = 64
	if utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	r := []rune(s)
	return string(r[:maxRunes]) + "..."
}
