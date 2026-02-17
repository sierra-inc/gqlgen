package graphql

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/validator/rules"
)

// Stress test to replicate 700ms+ production contention
// This simulates a more realistic production cold start scenario with:
// - High concurrency (500-2000 goroutines)
// - Many unique queries/cache keys
// - Sustained burst of traffic during cold start

const stressTestSchemaSDL = `
    interface Node {
        id: ID!
        name: String!
    }

    type User implements Node {
        id: ID!
        name: String!
        email: String
        profile: Profile
        posts: [Post!]!
    }

    type Profile {
        bio: String
        avatar: String
        website: String
        location: String
    }

    type Post implements Node {
        id: ID!
        name: String!
        title: String
        content: String
        author: User
        comments: [Comment!]!
        tags: [Tag!]!
    }

    type Comment implements Node {
        id: ID!
        name: String!
        text: String
        author: User
        post: Post
    }

    type Tag implements Node {
        id: ID!
        name: String!
        posts: [Post!]!
    }

    type Query {
        search(type: String): [Node!]!
        user(id: ID!): User
        post(id: ID!): Post
        feed: [Post!]!
    }
`

var stressTestSchema = gqlparser.MustLoadSchema(&ast.Source{
	Name:  "stress",
	Input: stressTestSchemaSDL,
})

// generateComplexQuery creates queries with varying complexity to hit different cache keys
func generateComplexQuery(variant int) (string, *ast.QueryDocument, *ast.OperationDefinition) {
	queries := []string{
		// Query 1: Search with fragments
		`query { search(type: "user") { id name ... on User { email profile { bio avatar } posts { id title } } } }`,
		// Query 2: Deep nesting
		`query { feed { id title author { id name profile { bio } posts { id title comments { id text author { id name } } } } } }`,
		// Query 3: Multiple fields
		`query { user(id: "1") { id name email profile { bio avatar website location } posts { id title content tags { id name } } } }`,
		// Query 4: Complex fragments
		`query { search(type: "post") { ... on Post { id title content author { id name email } comments { id text author { id name } } tags { id name } } } }`,
		// Query 5: Wide selection
		`query { feed { id title content author { id name email } comments { id text } tags { id name } } }`,
	}

	query := queries[variant%len(queries)]
	doc := gqlparser.MustLoadQueryWithRules(stressTestSchema, query, rules.NewDefaultRules())
	return query, doc, doc.Operations[0]
}

// BenchmarkCollectFieldsCache_ProductionStress simulates the 700ms contention scenario
// This test measures total wall-clock time under extreme concurrent load
func BenchmarkCollectFieldsCache_ProductionStress(b *testing.B) {
	concurrencyLevels := []int{500, 1000, 2000}

	for _, numGoroutines := range concurrencyLevels {
		b.Run(fmt.Sprintf("%d_goroutines", numGoroutines), func(b *testing.B) {
			b.ResetTimer()

			for i := 0; i < b.N; i++ {
				opCtx := &OperationContext{
					RawQuery:  "stress test",
					Variables: make(map[string]any),
				}

				start := time.Now()
				var wg sync.WaitGroup
				wg.Add(numGoroutines)

				// Track how long goroutines spend waiting
				var totalWaitTime atomic.Int64

				for j := 0; j < numGoroutines; j++ {
					go func(idx int) {
						defer wg.Done()

						goroutineStart := time.Now()

						// Each goroutine does multiple operations (simulating a real request)
						// Use different query variants to create many cache keys
						queryVariant := idx % 5
						_, doc, op := generateComplexQuery(queryVariant)

						opCtx.Doc = doc
						opCtx.Operation = op

						// Access multiple fields (typical for a real query)
						if len(op.SelectionSet) > 0 {
							for _, sel := range op.SelectionSet {
								if field, ok := sel.(*ast.Field); ok {
									// Collect fields for different satisfies
									_ = CollectFields(opCtx, field.SelectionSet, []string{"User"})
									_ = CollectFields(opCtx, field.SelectionSet, []string{"Post"})
									_ = CollectFields(opCtx, field.SelectionSet, []string{"Comment"})
									_ = CollectFields(opCtx, field.SelectionSet, nil)
								}
							}
						}

						elapsed := time.Since(goroutineStart)
						totalWaitTime.Add(int64(elapsed))
					}(j)
				}

				wg.Wait()
				wallClockTime := time.Since(start)
				avgWaitPerGoroutine := time.Duration(totalWaitTime.Load() / int64(numGoroutines))

				// Report metrics
				b.ReportMetric(float64(wallClockTime.Milliseconds()), "wall_clock_ms")
				b.ReportMetric(float64(avgWaitPerGoroutine.Microseconds()), "avg_wait_us")
				b.ReportMetric(float64(opCtx.collectFieldsCache.Len()), "cache_entries")
			}
		})
	}
}

// BenchmarkCollectFieldsCache_SyncMap_ProductionStress same test but with sync.Map
func BenchmarkCollectFieldsCache_SyncMap_ProductionStress(b *testing.B) {
	concurrencyLevels := []int{500, 1000, 2000}

	for _, numGoroutines := range concurrencyLevels {
		b.Run(fmt.Sprintf("%d_goroutines", numGoroutines), func(b *testing.B) {
			b.ResetTimer()

			for i := 0; i < b.N; i++ {
				cache := &collectFieldsCacheSyncMap{}
				opCtx := &OperationContext{
					RawQuery:  "stress test",
					Variables: make(map[string]any),
				}

				start := time.Now()
				var wg sync.WaitGroup
				wg.Add(numGoroutines)

				var totalWaitTime atomic.Int64

				for j := 0; j < numGoroutines; j++ {
					go func(idx int) {
						defer wg.Done()

						goroutineStart := time.Now()

						queryVariant := idx % 5
						_, doc, op := generateComplexQuery(queryVariant)

						opCtx.Doc = doc
						opCtx.Operation = op

						if len(op.SelectionSet) > 0 {
							for _, sel := range op.SelectionSet {
								if field, ok := sel.(*ast.Field); ok {
									_ = CollectFieldsSyncMap(cache, opCtx, field.SelectionSet, []string{"User"})
									_ = CollectFieldsSyncMap(cache, opCtx, field.SelectionSet, []string{"Post"})
									_ = CollectFieldsSyncMap(cache, opCtx, field.SelectionSet, []string{"Comment"})
									_ = CollectFieldsSyncMap(cache, opCtx, field.SelectionSet, nil)
								}
							}
						}

						elapsed := time.Since(goroutineStart)
						totalWaitTime.Add(int64(elapsed))
					}(j)
				}

				wg.Wait()
				wallClockTime := time.Since(start)
				avgWaitPerGoroutine := time.Duration(totalWaitTime.Load() / int64(numGoroutines))

				b.ReportMetric(float64(wallClockTime.Milliseconds()), "wall_clock_ms")
				b.ReportMetric(float64(avgWaitPerGoroutine.Microseconds()), "avg_wait_us")
				b.ReportMetric(float64(cache.Len()), "cache_entries")
			}
		})
	}
}

// TestCollectFieldsCache_MeasureContention is not a benchmark but a direct measurement
// of the contention issue to see actual wall-clock time
func TestCollectFieldsCache_MeasureContention(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping contention measurement in short mode")
	}

	tests := []struct {
		name           string
		numGoroutines  int
		targetTimeMs   int // Expected time if 700ms was seen in production
	}{
		{"moderate_contention", 500, 100},
		{"high_contention", 1000, 300},
		{"extreme_contention", 2000, 700},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("Testing with %d goroutines (expecting ~%dms of contention)", tt.numGoroutines, tt.targetTimeMs)

			// Test current mutex implementation
			opCtx := &OperationContext{
				RawQuery:  "contention test",
				Variables: make(map[string]any),
			}

			start := time.Now()
			var wg sync.WaitGroup
			wg.Add(tt.numGoroutines)

			for j := 0; j < tt.numGoroutines; j++ {
				go func(idx int) {
					defer wg.Done()

					queryVariant := idx % 5
					_, doc, op := generateComplexQuery(queryVariant)
					opCtx.Doc = doc
					opCtx.Operation = op

					if len(op.SelectionSet) > 0 {
						for _, sel := range op.SelectionSet {
							if field, ok := sel.(*ast.Field); ok {
								_ = CollectFields(opCtx, field.SelectionSet, []string{"User"})
								_ = CollectFields(opCtx, field.SelectionSet, []string{"Post"})
								_ = CollectFields(opCtx, field.SelectionSet, []string{"Comment"})
							}
						}
					}
				}(j)
			}

			wg.Wait()
			mutexTime := time.Since(start)

			// Test sync.Map implementation
			cache := &collectFieldsCacheSyncMap{}
			opCtxSync := &OperationContext{
				RawQuery:  "contention test",
				Variables: make(map[string]any),
			}

			start = time.Now()
			wg.Add(tt.numGoroutines)

			for j := 0; j < tt.numGoroutines; j++ {
				go func(idx int) {
					defer wg.Done()

					queryVariant := idx % 5
					_, doc, op := generateComplexQuery(queryVariant)
					opCtxSync.Doc = doc
					opCtxSync.Operation = op

					if len(op.SelectionSet) > 0 {
						for _, sel := range op.SelectionSet {
							if field, ok := sel.(*ast.Field); ok {
								_ = CollectFieldsSyncMap(cache, opCtxSync, field.SelectionSet, []string{"User"})
								_ = CollectFieldsSyncMap(cache, opCtxSync, field.SelectionSet, []string{"Post"})
								_ = CollectFieldsSyncMap(cache, opCtxSync, field.SelectionSet, []string{"Comment"})
							}
						}
					}
				}(j)
			}

			wg.Wait()
			syncMapTime := time.Since(start)

			improvement := float64(mutexTime-syncMapTime) / float64(mutexTime) * 100

			t.Logf("Mutex implementation:   %v", mutexTime)
			t.Logf("sync.Map implementation: %v", syncMapTime)
			t.Logf("Improvement: %.1f%% faster (saved %v)", improvement, mutexTime-syncMapTime)
			t.Logf("Cache entries: %d", opCtx.collectFieldsCache.Len())

			if mutexTime > time.Duration(tt.targetTimeMs)*time.Millisecond {
				t.Logf("✓ Successfully reproduced production-level contention (%dms+)", tt.targetTimeMs)
			} else {
				t.Logf("⚠ Did not fully reproduce production contention (got %v, expected %dms+)", mutexTime, tt.targetTimeMs)
			}
		})
	}
}
