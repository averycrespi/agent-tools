package contract

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProductBehaviorManifestSchema(t *testing.T) {
	assert.Equal(t, 1, ProductBehaviorManifestVersion)
	product := ProductBehaviorManifest()
	require.Len(t, product, 140)
	kindCounts := map[string]int{}
	for _, behavior := range product {
		kindCounts[behavior.Kind]++
	}
	assert.Equal(t, map[string]int{
		"capability": 31,
		"clause":     90,
		"criterion":  11,
		"lifecycle":  8,
	}, kindCounts)
	assert.Len(t, SecurityBehaviorManifest(), 18)
	assert.Len(t, DocumentationBehaviorManifest(), 40)
	assert.Len(t, PredecessorBehaviorManifest(), 18)
	assert.Len(t, EvidenceTierManifest(), 16)
}

func TestProductBehaviorManifestIDsAreStableAndDisjoint(t *testing.T) {
	validID := regexp.MustCompile(`^(product|security|docs|cli|frontend|tier)\.[a-z0-9]+(?:[._][a-z0-9]+)*$`)
	legacyPointer := regexp.MustCompile(`(?:^|[._-])(?:AC-[0-9]+|[TM][0-9]+|s[1-6])(?:$|[._-])`)
	seen := map[string]struct{}{}
	tiers := map[string]struct{}{}
	for _, tier := range EvidenceTierManifest() {
		tiers[tier.ID] = struct{}{}
	}
	check := func(id, owner string) {
		t.Helper()
		assert.Regexp(t, validID, id)
		assert.NotRegexp(t, legacyPointer, id)
		assert.Regexp(t, validID, owner)
		assert.NotRegexp(t, legacyPointer, owner)
		_, knownOwner := tiers[owner]
		assert.True(t, knownOwner, owner)
		_, duplicate := seen[id]
		assert.False(t, duplicate, id)
		seen[id] = struct{}{}
	}
	for _, behavior := range ProductBehaviorManifest() {
		check(behavior.ID, behavior.EvidenceOwner)
	}
	for _, behavior := range SecurityBehaviorManifest() {
		check(behavior.ID, behavior.EvidenceOwner)
	}
	for _, behavior := range DocumentationBehaviorManifest() {
		check(behavior.ID, behavior.EvidenceOwner)
		assert.NotEmpty(t, behavior.CanonicalOwner, behavior.ID)
	}
	for _, behavior := range PredecessorBehaviorManifest() {
		check(behavior.ID, behavior.EvidenceOwner)
	}
	for _, owner := range EvidenceTierManifest() {
		check(owner.ID, owner.ID)
	}
	for index := 1; index <= 10; index++ {
		cleanupID := "cleanup." + string(rune('a'+index-1))
		_, collision := seen[cleanupID]
		assert.False(t, collision, cleanupID)
	}
}

func TestProductBehaviorManifestReturnsIndependentValues(t *testing.T) {
	product := ProductBehaviorManifest()
	product[0].ID = "changed"
	assert.NotEqual(t, "changed", ProductBehaviorManifest()[0].ID)

	documentation := DocumentationBehaviorManifest()
	documentation[0].CanonicalOwner = "changed"
	assert.NotEqual(t, "changed", DocumentationBehaviorManifest()[0].CanonicalOwner)
}
