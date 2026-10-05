package httpcredentials

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"golang.org/x/text/unicode/norm"
)

var (
	ErrInvalidCursor = errors.New("invalid HTTP credential cursor")
	ErrStaleCursor   = errors.New("stale HTTP credential cursor")
)

// CollectionQuery recognizes safe metadata, never material or boundary authority.
type CollectionQuery struct{ Name, Boundary, Recipe, Status, Sort, Direction string }

func (q CollectionQuery) Validate() bool {
	for _, value := range []string{q.Name, q.Boundary, q.Recipe} {
		if !utf8.ValidString(value) || len(value) > 256 || strings.IndexFunc(value, func(r rune) bool { return unicode.IsControl(r) || unicode.In(r, unicode.Cf) }) >= 0 {
			return false
		}
	}
	return slices.Contains([]string{"", "configured", "unavailable"}, q.Status) &&
		slices.Contains([]string{"", "name", "boundary", "recipe", "status"}, q.Sort) &&
		(q.Direction == "" || q.Sort != "" && slices.Contains([]string{"ascending", "descending"}, q.Direction))
}

type collectionCursor struct {
	Version int
	Binding string
	After   string
	Expires int64
	Seal    string
}

// SelectPage filters the bounded authoritative inventory before slicing it. Its
// cursor authenticates query, safe metadata/material evidence, order and expiry
// under a private API-owner key that is never serialized or persisted.
func SelectPage(items []Resource, q CollectionQuery, cursor string, limit int, now time.Time, cursorKey string) (contract.QueryCollection[Resource], error) {
	var page contract.QueryCollection[Resource]
	if len(cursorKey) < 32 || !q.Validate() || limit < 1 || limit > 100 || len(items) > contract.HTTPPolicyCredentials {
		return page, ErrInvalid
	}
	if q.Sort != "" && q.Direction == "" {
		q.Direction = "ascending"
	}
	metadata, err := json.Marshal(struct {
		Query CollectionQuery
		Items []Resource
	}{q, items})
	if err != nil {
		return page, err
	}
	digest := sha256.Sum256(metadata)
	binding := base64.RawURLEncoding.EncodeToString(digest[:])
	position := collectionCursor{Version: 2, Binding: binding, Expires: now.Add(contract.AuthorizationCursorLifetime).Unix()}
	if cursor != "" {
		if len(cursor) > 512 {
			return page, ErrInvalidCursor
		}
		raw, err := base64.RawURLEncoding.DecodeString(cursor)
		if err != nil {
			return page, ErrInvalidCursor
		}
		// Existing unfiltered clients may finish their v1 traversal.
		legacy := strings.Split(string(raw), "\x00")
		if len(legacy) == 3 && legacy[0] == "v1" && legacy[1] == "http_credentials" && contract.ValidAuditID(legacy[2]) && q == (CollectionQuery{}) {
			position.After = legacy[2]
		} else {
			if json.Unmarshal(raw, &position) != nil || position.Version != 2 || !contract.ValidAuditID(position.After) {
				return page, ErrInvalidCursor
			}
			canonical, _ := json.Marshal(position)
			if string(canonical) != string(raw) {
				return page, ErrInvalidCursor
			}
			seal := position.Seal
			sealCollectionCursor(&position, cursorKey)
			if !hmac.Equal([]byte(seal), []byte(position.Seal)) || position.Binding != binding || position.Expires <= now.Unix() || position.Expires > now.Add(contract.AuthorizationCursorLifetime).Unix() {
				return page, ErrStaleCursor
			}
		}
	}
	matched := make([]Resource, 0, len(items))
	for _, item := range items {
		if (q.Status == "" || (q.Status == "configured") == item.Available) && recognition(item.Name, item.ID, q.Name) && recognition(item.Boundary.Host+":"+strconv.Itoa(int(item.Boundary.Port)), "", q.Boundary) && recognition(item.Recipe.Header+" "+item.Recipe.Prefix, "", q.Recipe) {
			matched = append(matched, item)
		}
	}
	if q.Sort != "" {
		slices.SortFunc(matched, func(a, b Resource) int {
			order := strings.Compare(credentialSortValue(a, q.Sort), credentialSortValue(b, q.Sort))
			if q.Direction == "descending" {
				order = -order
			}
			if order == 0 {
				return strings.Compare(a.ID, b.ID)
			}
			return order
		})
	}
	start := 0
	if position.After != "" {
		found := false
		for i, item := range matched {
			if item.ID == position.After {
				start = i + 1
				found = true
				break
			}
		}
		if !found {
			return page, ErrStaleCursor
		}
	}
	if start > 0 && start >= len(matched) {
		return page, ErrStaleCursor
	}
	end := min(start+limit, len(matched))
	if end < len(matched) {
		position.After = matched[end-1].ID
		sealCollectionCursor(&position, cursorKey)
		raw, err := json.Marshal(position)
		if err != nil {
			return page, err
		}
		next := base64.RawURLEncoding.EncodeToString(raw)
		page.NextCursor = &next
	}
	page.Items = matched[start:end]
	page.CollectionRange = contract.CollectionRange{TotalCount: len(matched), Offset: start}
	return page, nil
}

func credentialSortValue(item Resource, key string) string {
	switch key {
	case "name":
		return normalizeRecognition(item.Name)
	case "boundary":
		return normalizeRecognition(item.Boundary.Host) + ":" + strconv.Itoa(int(item.Boundary.Port))
	case "recipe":
		return normalizeRecognition(item.Recipe.Header + " " + item.Recipe.Prefix)
	default:
		if item.Available {
			return "configured"
		}
		return "unavailable"
	}
}

func sealCollectionCursor(cursor *collectionCursor, key string) {
	cursor.Seal = ""
	contents, _ := json.Marshal(cursor)
	mac := hmac.New(sha256.New, []byte(key))
	_, _ = mac.Write(contents)
	cursor.Seal = base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func normalizeRecognition(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsMark(r) {
			return -1
		}
		return unicode.ToLower(r)
	}, norm.NFKD.String(value))
}
func recognition(value, id, query string) bool {
	query = strings.TrimSpace(query)
	if query == "" || id != "" && strings.Contains(id, query) {
		return true
	}
	candidate := normalizeRecognition(value)
	words := strings.FieldsFunc(candidate, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) })
	for _, token := range strings.Fields(normalizeRecognition(query)) {
		if strings.Contains(candidate, token) {
			continue
		}
		if len([]rune(token)) < 4 || strings.IndexFunc(token, unicode.IsDigit) >= 0 || !slices.ContainsFunc(words, func(word string) bool { return oneEdit([]rune(word), []rune(token)) }) {
			return false
		}
	}
	return true
}
func oneEdit(a, b []rune) bool {
	if len(a)-len(b) > 1 || len(b)-len(a) > 1 {
		return false
	}
	i, j, edits := 0, 0, 0
	for i < len(a) && j < len(b) {
		if a[i] == b[j] {
			i++
			j++
			continue
		}
		edits++
		if edits > 1 {
			return false
		}
		switch {
		case len(a) == len(b) && i+1 < len(a) && j+1 < len(b) && a[i] == b[j+1] && a[i+1] == b[j]:
			i += 2
			j += 2
		case len(a) > len(b):
			i++
		case len(b) > len(a):
			j++
		default:
			i++
			j++
		}
	}
	return edits+len(a)-i+len(b)-j <= 1
}
