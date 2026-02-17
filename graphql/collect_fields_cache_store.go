package graphql

import (
	"hash/fnv"
	"reflect"
	"sync"

	"github.com/vektah/gqlparser/v2/ast"
)

// collectFieldsCacheKey is the cache key for CollectFields results.
type collectFieldsCacheKey struct {
	selectionPtr  uintptr // Pointer to the underlying SelectionSet data
	selectionLen  int     // Length of the selection set
	satisfiesHash uint64  // Hash of the satisfies array
}

// collectFieldsCacheStore manages CollectFields cache entries safely.
// Uses sync.Map for better performance under concurrent write scenarios (cold start).
type collectFieldsCacheStore struct {
	items sync.Map // map[collectFieldsCacheKey][]CollectedField
}

// Get returns the cached result for the key if present.
func (s *collectFieldsCacheStore) Get(key collectFieldsCacheKey) ([]CollectedField, bool) {
	val, ok := s.items.Load(key)
	if !ok {
		return nil, false
	}
	return val.([]CollectedField), true
}

// Add stores the value when absent and returns the cached value.
// LoadOrStore is used to handle concurrent writes efficiently.
func (s *collectFieldsCacheStore) Add(
	key collectFieldsCacheKey,
	value []CollectedField,
) []CollectedField {
	// LoadOrStore is atomic and optimized for concurrent access
	// It only stores if the key doesn't exist, otherwise returns the existing value
	actual, _ := s.items.LoadOrStore(key, value)
	return actual.([]CollectedField)
}

// Len returns the number of cached entries.
// Note: This requires iterating over all entries and is O(n).
func (s *collectFieldsCacheStore) Len() int {
	count := 0
	s.items.Range(func(_, _ any) bool {
		count++
		return true
	})
	return count
}

// makeCollectFieldsCacheKey generates a cache key for CollectFields.
func makeCollectFieldsCacheKey(selSet ast.SelectionSet, satisfies []string) collectFieldsCacheKey {
	var selectionPtr uintptr
	if selSet != nil {
		selectionPtr = reflect.ValueOf(selSet).Pointer()
	}

	h := fnv.New64a()
	for _, s := range satisfies {
		h.Write([]byte(s))
		h.Write([]byte{0})
	}

	return collectFieldsCacheKey{
		selectionPtr:  selectionPtr,
		selectionLen:  len(selSet),
		satisfiesHash: h.Sum64(),
	}
}
