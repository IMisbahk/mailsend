package imap

import (
	"bytes"
	"encoding/base64"
	"html"
	"io"
	"mime/quotedprintable"
	"regexp"
	"strings"

	"github.com/emersion/go-imap/v2"
)

const snippetLimit = 140

var (
	htmlScriptPattern  = regexp.MustCompile(`(?is)<script\b[^>]*>.*?</script>`)
	htmlStylePattern   = regexp.MustCompile(`(?is)<style\b[^>]*>.*?</style>`)
	htmlCommentPattern = regexp.MustCompile(`(?s)<!--.*?-->`)
	htmlBreakPattern   = regexp.MustCompile(`(?i)<br\s*/?>`)
	htmlBlockPattern   = regexp.MustCompile(`(?i)</(p|div|tr|h[1-6]|li|blockquote)>`)
	htmlTagPattern     = regexp.MustCompile(`(?s)<[^>]*>`)
)

type bodyPart struct {
	path []int
	part *imap.BodyStructureSinglePart
}

func collectParts(structure imap.BodyStructure) []bodyPart {
	var parts []bodyPart
	walkParts(structure, nil, &parts)
	return parts
}

func walkParts(structure imap.BodyStructure, path []int, parts *[]bodyPart) {
	switch part := structure.(type) {
	case *imap.BodyStructureSinglePart:
		section := path
		if len(section) == 0 {
			section = []int{1}
		}
		copied := append([]int(nil), section...)
		*parts = append(*parts, bodyPart{path: copied, part: part})
	case *imap.BodyStructureMultiPart:
		for i, child := range part.Children {
			walkParts(child, appendPath(path, i+1), parts)
		}
	}
}

func appendPath(path []int, partNum int) []int {
	next := make([]int, len(path)+1)
	copy(next, path)
	next[len(path)] = partNum
	return next
}

func chooseBody(parts []bodyPart) (bodyPart, bool) {
	if part, ok := firstBody(parts, "text/plain", false); ok {
		return part, true
	}
	if part, ok := firstBody(parts, "text/html", false); ok {
		return part, true
	}
	if part, ok := firstBody(parts, "text/plain", true); ok {
		return part, true
	}
	return firstBody(parts, "text/html", true)
}

func firstBody(parts []bodyPart, media string, allowAttachment bool) (bodyPart, bool) {
	for _, part := range parts {
		if part.part == nil || !strings.EqualFold(part.part.MediaType(), media) {
			continue
		}
		if !allowAttachment && partIsAttachment(part.part) {
			continue
		}
		return part, true
	}
	return bodyPart{}, false
}

func partIsAttachment(part *imap.BodyStructureSinglePart) bool {
	if part == nil {
		return false
	}
	if disp := part.Disposition(); disp != nil && strings.EqualFold(disp.Value, "attachment") {
		return true
	}
	media := strings.ToLower(part.MediaType())
	return media != "text/plain" && media != "text/html"
}

func attachmentName(part *imap.BodyStructureSinglePart) string {
	name := ""
	if part != nil {
		name = strings.TrimSpace(part.Filename())
		if name == "" {
			for key, value := range part.Params {
				if strings.EqualFold(key, "name") && strings.TrimSpace(value) != "" {
					name = value
					break
				}
			}
		}
	}
	name = strings.ReplaceAll(name, "\r", "")
	name = strings.ReplaceAll(name, "\n", "")
	name = strings.TrimSpace(name)
	if name == "" {
		return "unnamed"
	}
	return name
}

func attachmentsFrom(parts []bodyPart) []Attachment {
	out := make([]Attachment, 0)
	for _, part := range parts {
		if !partIsAttachment(part.part) {
			continue
		}
		out = append(out, Attachment{Name: attachmentName(part.part), Size: int64(part.part.Size)})
	}
	return out
}

func decodeTransfer(encoding string, raw []byte) ([]byte, error) {
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "", "7bit", "8bit", "binary":
		return raw, nil
	case "quoted-printable":
		return io.ReadAll(quotedprintable.NewReader(bytes.NewReader(raw)))
	case "base64":
		cleaned := bytes.Map(func(r rune) rune {
			if r == ' ' || r == '\n' || r == '\r' || r == '\t' {
				return -1
			}
			return r
		}, raw)
		return io.ReadAll(base64.NewDecoder(base64.StdEncoding, bytes.NewReader(cleaned)))
	default:
		return raw, nil
	}
}

func partText(part *imap.BodyStructureSinglePart, raw []byte) (string, error) {
	encoding := ""
	htmlBody := false
	if part != nil {
		encoding = part.Encoding
		htmlBody = strings.EqualFold(part.MediaType(), "text/html")
	}
	decoded, err := decodeTransfer(encoding, raw)
	if err != nil {
		return "", err
	}
	text := strings.ToValidUTF8(string(decoded), "")
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	if htmlBody {
		text = stripHTML(text)
	}
	return strings.TrimSpace(text), nil
}

func plainSnippet(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	return truncateRunes(text, snippetLimit)
}

func truncateRunes(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	if limit < 4 {
		return string(runes[:limit])
	}
	return string(runes[:limit-3]) + "..."
}

func stripHTML(value string) string {
	value = htmlScriptPattern.ReplaceAllString(value, " ")
	value = htmlStylePattern.ReplaceAllString(value, " ")
	value = htmlCommentPattern.ReplaceAllString(value, " ")
	value = htmlBreakPattern.ReplaceAllString(value, "\n")
	value = htmlBlockPattern.ReplaceAllString(value, "\n")
	value = htmlTagPattern.ReplaceAllString(value, " ")
	return html.UnescapeString(value)
}

func formatAddress(addr imap.Address) string {
	email := addr.Addr()
	name := strings.TrimSpace(addr.Name)
	name = strings.ReplaceAll(strings.ReplaceAll(name, "\r", ""), "\n", "")
	if name == "" {
		return email
	}
	if email == "" {
		return name
	}
	return name + " <" + email + ">"
}

func formatAddresses(list []imap.Address) []string {
	out := make([]string, 0, len(list))
	for _, addr := range list {
		if addr.IsGroupStart() || addr.IsGroupEnd() {
			continue
		}
		formatted := formatAddress(addr)
		if formatted != "" {
			out = append(out, formatted)
		}
	}
	return out
}

func joinAddresses(list []imap.Address) string {
	return strings.Join(formatAddresses(list), ", ")
}

func hasFlag(flags []imap.Flag, want imap.Flag) bool {
	for _, flag := range flags {
		if strings.EqualFold(string(flag), string(want)) {
			return true
		}
	}
	return false
}

func envelopeHasAddress(env *imap.Envelope, email string) bool {
	if env == nil {
		return false
	}
	return addressListHas(env.To, email) || addressListHas(env.Cc, email) || addressListHas(env.Bcc, email)
}

func addressListHas(list []imap.Address, email string) bool {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return false
	}
	for _, addr := range list {
		if strings.EqualFold(addr.Addr(), email) {
			return true
		}
	}
	return false
}
