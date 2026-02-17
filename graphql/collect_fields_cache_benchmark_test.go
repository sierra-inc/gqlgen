package graphql

import (
	"fmt"
	"sync"
	"testing"

	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/validator/rules"
)

// Benchmark schema and query setup
const benchmarkSchemaSDL = `
    interface Node {
        id: ID!
        name: String!
        description: String
        createdAt: String
        updatedAt: String
    }

    type User implements Node {
        id: ID!
        name: String!
        description: String
        createdAt: String
        updatedAt: String
        email: String
        username: String
        avatar: String
        role: String
    }

    type Post implements Node {
        id: ID!
        name: String!
        description: String
        createdAt: String
        updatedAt: String
        title: String
        content: String
        author: User
        tags: [String!]
    }

    type Comment implements Node {
        id: ID!
        name: String!
        description: String
        createdAt: String
        updatedAt: String
        text: String
        author: User
        post: Post
    }

    type Query {
        search: [Node!]!
        users: [User!]!
        posts: [Post!]!
        comments: [Comment!]!
    }
`

const benchmarkQuery = `
    query {
        search {
            id
            name
            description
            ... on User {
                email
                username
                avatar
                role
            }
            ... on Post {
                title
                content
                author {
                    id
                    name
                    email
                }
                tags
            }
            ... on Comment {
                text
                author {
                    id
                    name
                }
                post {
                    id
                    title
                }
            }
        }
        users {
            id
            name
            email
            username
        }
        posts {
            id
            title
            content
        }
    }
`

var (
	benchmarkSchema *ast.Schema
	benchmarkDoc    *ast.QueryDocument
	benchmarkOp     *ast.OperationDefinition
)

func init() {
	benchmarkSchema = gqlparser.MustLoadSchema(&ast.Source{
		Name:  "benchmark",
		Input: benchmarkSchemaSDL,
	})
	benchmarkDoc = gqlparser.MustLoadQueryWithRules(benchmarkSchema, benchmarkQuery, rules.NewDefaultRules())
	benchmarkOp = benchmarkDoc.Operations[0]
}

// benchName formats the benchmark name for a given number of goroutines
func benchName(n int) string {
	return fmt.Sprintf("%d_goroutines", n)
}

// BenchmarkCollectFieldsCache_ColdStart_Sequential measures baseline performance
// with sequential access (no contention)
func BenchmarkCollectFieldsCache_ColdStart_Sequential(b *testing.B) {
	searchField := benchmarkOp.SelectionSet[0].(*ast.Field)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		opCtx := &OperationContext{
			RawQuery:  benchmarkQuery,
			Doc:       benchmarkDoc,
			Operation: benchmarkOp,
		}

		// Cold start - cache is empty, simulate different satisfies
		_ = CollectFields(opCtx, searchField.SelectionSet, []string{"User"})
		_ = CollectFields(opCtx, searchField.SelectionSet, []string{"Post"})
		_ = CollectFields(opCtx, searchField.SelectionSet, []string{"Comment"})
	}
}

// BenchmarkCollectFieldsCache_ColdStart_Concurrent simulates the production issue:
// multiple goroutines hitting the cache simultaneously during cold start
func BenchmarkCollectFieldsCache_ColdStart_Concurrent(b *testing.B) {
	searchField := benchmarkOp.SelectionSet[0].(*ast.Field)

	// Test different concurrency levels to show lock contention
	for _, numGoroutines := range []int{10, 50, 100, 200} {
		b.Run(benchName(numGoroutines), func(b *testing.B) {
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				opCtx := &OperationContext{
					RawQuery:  benchmarkQuery,
					Doc:       benchmarkDoc,
					Operation: benchmarkOp,
				}

				var wg sync.WaitGroup
				wg.Add(numGoroutines)

				// Simulate cold start: all goroutines try to populate cache simultaneously
				for j := 0; j < numGoroutines; j++ {
					go func(idx int) {
						defer wg.Done()
						// Each goroutine accesses different cache keys
						satisfies := []string{"User", "Post", "Comment"}
						satisfy := satisfies[idx%len(satisfies)]
						_ = CollectFields(opCtx, searchField.SelectionSet, []string{satisfy})
					}(j)
				}

				wg.Wait()
			}
		})
	}
}

// BenchmarkCollectFieldsCache_WarmCache measures performance when cache is already populated
func BenchmarkCollectFieldsCache_WarmCache_Concurrent(b *testing.B) {
	searchField := benchmarkOp.SelectionSet[0].(*ast.Field)

	// Pre-populate cache
	opCtx := &OperationContext{
		RawQuery:  benchmarkQuery,
		Doc:       benchmarkDoc,
		Operation: benchmarkOp,
	}
	_ = CollectFields(opCtx, searchField.SelectionSet, []string{"User"})
	_ = CollectFields(opCtx, searchField.SelectionSet, []string{"Post"})
	_ = CollectFields(opCtx, searchField.SelectionSet, []string{"Comment"})

	for _, numGoroutines := range []int{10, 50, 100, 200} {
		b.Run(benchName(numGoroutines), func(b *testing.B) {
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				var wg sync.WaitGroup
				wg.Add(numGoroutines)

				for j := 0; j < numGoroutines; j++ {
					go func(idx int) {
						defer wg.Done()
						satisfies := []string{"User", "Post", "Comment"}
						satisfy := satisfies[idx%len(satisfies)]
						_ = CollectFields(opCtx, searchField.SelectionSet, []string{satisfy})
					}(j)
				}

				wg.Wait()
			}
		})
	}
}

// Test sync.Map implementation
type collectFieldsCacheSyncMap struct {
	items sync.Map // map[collectFieldsCacheKey][]CollectedField
}

func (s *collectFieldsCacheSyncMap) Get(key collectFieldsCacheKey) ([]CollectedField, bool) {
	val, ok := s.items.Load(key)
	if !ok {
		return nil, false
	}
	return val.([]CollectedField), true
}

func (s *collectFieldsCacheSyncMap) Add(
	key collectFieldsCacheKey,
	value []CollectedField,
) []CollectedField {
	// LoadOrStore is atomic and handles concurrent writes efficiently
	actual, _ := s.items.LoadOrStore(key, value)
	return actual.([]CollectedField)
}

func (s *collectFieldsCacheSyncMap) Len() int {
	count := 0
	s.items.Range(func(_, _ any) bool {
		count++
		return true
	})
	return count
}

// CollectFieldsSyncMap is the same as CollectFields but uses sync.Map
func CollectFieldsSyncMap(
	cache *collectFieldsCacheSyncMap,
	reqCtx *OperationContext,
	selSet ast.SelectionSet,
	satisfies []string,
) []CollectedField {
	cacheKey := makeCollectFieldsCacheKey(selSet, satisfies)

	if cached, ok := cache.Get(cacheKey); ok {
		return cached
	}

	result := collectFields(reqCtx, selSet, satisfies, map[string]bool{})

	return cache.Add(cacheKey, result)
}

// BenchmarkCollectFieldsCache_SyncMap_ColdStart_Concurrent tests sync.Map implementation
func BenchmarkCollectFieldsCache_SyncMap_ColdStart_Concurrent(b *testing.B) {
	searchField := benchmarkOp.SelectionSet[0].(*ast.Field)

	for _, numGoroutines := range []int{10, 50, 100, 200} {
		b.Run(benchName(numGoroutines), func(b *testing.B) {
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				cache := &collectFieldsCacheSyncMap{}
				opCtx := &OperationContext{
					RawQuery:  benchmarkQuery,
					Doc:       benchmarkDoc,
					Operation: benchmarkOp,
				}

				var wg sync.WaitGroup
				wg.Add(numGoroutines)

				// Simulate cold start: all goroutines try to populate cache simultaneously
				for j := 0; j < numGoroutines; j++ {
					go func(idx int) {
						defer wg.Done()
						satisfies := []string{"User", "Post", "Comment"}
						satisfy := satisfies[idx%len(satisfies)]
						_ = CollectFieldsSyncMap(cache, opCtx, searchField.SelectionSet, []string{satisfy})
					}(j)
				}

				wg.Wait()
			}
		})
	}
}

// BenchmarkCollectFieldsCache_SyncMap_WarmCache_Concurrent tests sync.Map with warm cache
func BenchmarkCollectFieldsCache_SyncMap_WarmCache_Concurrent(b *testing.B) {
	searchField := benchmarkOp.SelectionSet[0].(*ast.Field)

	// Pre-populate cache
	cache := &collectFieldsCacheSyncMap{}
	opCtx := &OperationContext{
		RawQuery:  benchmarkQuery,
		Doc:       benchmarkDoc,
		Operation: benchmarkOp,
	}
	_ = CollectFieldsSyncMap(cache, opCtx, searchField.SelectionSet, []string{"User"})
	_ = CollectFieldsSyncMap(cache, opCtx, searchField.SelectionSet, []string{"Post"})
	_ = CollectFieldsSyncMap(cache, opCtx, searchField.SelectionSet, []string{"Comment"})

	for _, numGoroutines := range []int{10, 50, 100, 200} {
		b.Run(benchName(numGoroutines), func(b *testing.B) {
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				var wg sync.WaitGroup
				wg.Add(numGoroutines)

				for j := 0; j < numGoroutines; j++ {
					go func(idx int) {
						defer wg.Done()
						satisfies := []string{"User", "Post", "Comment"}
						satisfy := satisfies[idx%len(satisfies)]
						_ = CollectFieldsSyncMap(cache, opCtx, searchField.SelectionSet, []string{satisfy})
					}(j)
				}

				wg.Wait()
			}
		})
	}
}
