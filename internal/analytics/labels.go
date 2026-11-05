package analytics

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

var (
	reTag     = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,31}$`)
	reAddress = regexp.MustCompile(`^[GC][A-Z2-7]{55}$|^M[A-Z2-7]{68}$`)
)

// MaxNoteLen bounds label notes.
const MaxNoteLen = 280

// NormalizeTag lowercases a tag and strips a leading '#'.
func NormalizeTag(t string) string {
	return strings.ToLower(strings.TrimPrefix(strings.TrimSpace(t), "#"))
}

// IsTag reports whether t (normalized) is a valid tag.
func IsTag(t string) bool { return reTag.MatchString(t) }

// IsAddress reports whether a is a G…, M… or C… strkey.
func IsAddress(a string) bool { return reAddress.MatchString(a) }

// ErrInvalidLabel wraps label validation failures.
var ErrInvalidLabel = errors.New("invalid label")

// Label is a user-defined tag on an address.
type Label struct {
	Address string `json:"address"`
	Tag     string `json:"tag"`
	Note    string `json:"note"`
}

// NormalizeLabel validates and normalizes a label for storage.
func NormalizeLabel(l Label) (Label, error) {
	l.Address = strings.TrimSpace(l.Address)
	l.Tag = NormalizeTag(l.Tag)
	l.Note = SanitizeNote(l.Note)
	switch {
	case !IsAddress(l.Address):
		return l, fmt.Errorf("%w: address must be a G…, M… or C… strkey", ErrInvalidLabel)
	case !IsTag(l.Tag):
		return l, fmt.Errorf("%w: tag must match %s", ErrInvalidLabel, reTag)
	case len([]rune(l.Note)) > MaxNoteLen:
		return l, fmt.Errorf("%w: note is limited to %d characters", ErrInvalidLabel, MaxNoteLen)
	}
	return l, nil
}

// SanitizeNote keeps valid UTF-8 and drops control characters except newlines.
func SanitizeNote(s string) string {
	return strings.Map(func(r rune) rune {
		if r == 0 || (r < 32 && r != '\n') {
			return -1
		}
		return r
	}, strings.ToValidUTF8(s, ""))
}

// AttachLabels adds a "labels" list to rows whose key column is a labelled address.
func AttachLabels(rows []map[string]any, key string, labels map[string][]string) {
	for _, r := range rows {
		if a, ok := r[key].(string); ok && len(labels[a]) > 0 {
			r["labels"] = labels[a]
		}
	}
}
