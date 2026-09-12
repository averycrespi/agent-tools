package authorization

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
)

func (repository *Repository) sealCursor(cursor *SnapshotCursor) {
	cursor.Seal = ""
	contents, _ := json.Marshal(cursor)
	mac := hmac.New(sha256.New, repository.cursorKey[:])
	_, _ = mac.Write(contents)
	cursor.Seal = base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (repository *Repository) authenticCursor(cursor SnapshotCursor) bool {
	if cursor.Expires <= repository.clock.Now().Unix() {
		return false
	}
	seal := cursor.Seal
	repository.sealCursor(&cursor)
	return hmac.Equal([]byte(seal), []byte(cursor.Seal))
}
