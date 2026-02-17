package graphql

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/vektah/gqlparser/v2/ast"
)

// This test focuses on WRITE LOCK CONTENTION specifically
// The key insight: during cold start, ALL goroutines have cache misses
// and ALL try to acquire the write lock to add their results.
// With RWMutex, they queue up serially. With sync.Map, LoadOrStore is more efficient.

// TestCollectFieldsCache_WriteLockContention simulates the exact production scenario:
// - Cold start (empty cache)
// - All goroutines have unique keys (all cache misses)
// - All goroutines try to write simultaneously
func TestCollectFieldsCache_WriteLockContention(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping write lock contention test in short mode")
	}

	tests := []struct {
		name          string
		numGoroutines int
		numUniqueKeys int // Number of different cache keys
		targetTimeMs  int
	}{
		{"moderate_write_burst", 500, 100, 50},
		{"high_write_burst", 1000, 200, 150},
		{"extreme_write_burst", 5000, 500, 700},    // This should hit 700ms+
		{"massive_write_burst", 10000, 1000, 1000}, // Even more extreme
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("Testing %d goroutines writing %d unique keys (targeting %dms)",
				tt.numGoroutines, tt.numUniqueKeys, tt.targetTimeMs)

			// Generate unique queries for different cache keys
			queries := make([]*ast.Field, tt.numUniqueKeys)
			for i := 0; i < tt.numUniqueKeys; i++ {
				_, op := generateComplexQuery(i)
				if len(op.SelectionSet) > 0 {
					queries[i], _ = op.SelectionSet[0].(*ast.Field)
				}
			}

			// Test mutex implementation - all writes queue up on the write lock
			doc0, op0 := generateComplexQuery(0)
			opCtx := &OperationContext{
				RawQuery:  "write contention test",
				Variables: make(map[string]any),
				Doc:       doc0,
				Operation: op0,
			}

			start := time.Now()
			var wg sync.WaitGroup
			wg.Add(tt.numGoroutines)

			for j := 0; j < tt.numGoroutines; j++ {
				go func(idx int) {
					defer wg.Done()

					// Each goroutine accesses a unique key (cache miss → write lock contention)
					queryIdx := idx % tt.numUniqueKeys
					field := queries[queryIdx]
					if field == nil {
						return
					}

					// Different satisfies for more unique keys
					satisfies := [][]string{
						{"User"},
						{"Post"},
						{"Comment"},
						{"Tag"},
						{"User", "Post"},
						{"Post", "Comment"},
						nil,
					}
					satisfy := satisfies[idx%len(satisfies)]

					_ = CollectFields(opCtx, field.SelectionSet, satisfy)
				}(j)
			}

			wg.Wait()
			mutexTime := time.Since(start)

			// Test sync.Map implementation - LoadOrStore is optimized for concurrent writes
			cache := &collectFieldsCacheSyncMap{}
			opCtxSync := &OperationContext{
				RawQuery:  "write contention test",
				Variables: make(map[string]any),
				Doc:       doc0,
				Operation: op0,
			}

			start = time.Now()
			wg.Add(tt.numGoroutines)

			for j := 0; j < tt.numGoroutines; j++ {
				go func(idx int) {
					defer wg.Done()

					queryIdx := idx % tt.numUniqueKeys
					field := queries[queryIdx]
					if field == nil {
						return
					}

					satisfies := [][]string{
						{"User"},
						{"Post"},
						{"Comment"},
						{"Tag"},
						{"User", "Post"},
						{"Post", "Comment"},
						nil,
					}
					satisfy := satisfies[idx%len(satisfies)]

					_ = CollectFieldsSyncMap(cache, opCtxSync, field.SelectionSet, satisfy)
				}(j)
			}

			wg.Wait()
			syncMapTime := time.Since(start)

			// Calculate improvement
			improvement := float64(mutexTime-syncMapTime) / float64(mutexTime) * 100
			savedTime := mutexTime - syncMapTime

			t.Logf("")
			t.Logf("Results:")
			t.Logf("  Mutex (RWLock):     %v", mutexTime)
			t.Logf("  sync.Map:           %v", syncMapTime)
			t.Logf("  Improvement:        %.1f%% faster", improvement)
			t.Logf("  Time saved:         %v", savedTime)
			t.Logf("  Cache entries:      %d", opCtx.collectFieldsCache.Len())

			if mutexTime > time.Duration(tt.targetTimeMs)*time.Millisecond {
				t.Logf("")
				t.Logf(
					"✓ Successfully reproduced production-level contention (%dms+)",
					tt.targetTimeMs,
				)
				if savedTime > 100*time.Millisecond {
					t.Logf("✓ sync.Map would save %v in this scenario!", savedTime)
					t.Logf(
						"  That's %.0fms less latency for your users",
						float64(savedTime.Milliseconds()),
					)
				}
			} else {
				t.Logf("")
				t.Logf("⚠ Contention: %v (target was %dms+)", mutexTime, tt.targetTimeMs)
				if improvement > 0 {
					t.Logf("  But still %.1f%% improvement with sync.Map", improvement)
				}
			}
		})
	}
}

// Benchmark version for performance metrics
func BenchmarkCollectFieldsCache_WriteLockContention(b *testing.B) {
	configs := []struct {
		goroutines int
		uniqueKeys int
	}{
		{1000, 200},
		{5000, 500},
		{10000, 1000},
	}

	for _, cfg := range configs {
		// Generate queries
		queries := make([]*ast.Field, cfg.uniqueKeys)
		for i := 0; i < cfg.uniqueKeys; i++ {
			_, op := generateComplexQuery(i)
			if len(op.SelectionSet) > 0 {
				queries[i], _ = op.SelectionSet[0].(*ast.Field)
			}
		}

		b.Run(fmt.Sprintf("mutex_%dg_%dk", cfg.goroutines, cfg.uniqueKeys), func(b *testing.B) {
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				doc, op := generateComplexQuery(0)
				opCtx := &OperationContext{
					RawQuery:  "bench",
					Variables: make(map[string]any),
					Doc:       doc,
					Operation: op,
				}

				start := time.Now()
				var wg sync.WaitGroup
				wg.Add(cfg.goroutines)

				for j := 0; j < cfg.goroutines; j++ {
					go func(idx int) {
						defer wg.Done()
						queryIdx := idx % cfg.uniqueKeys
						field := queries[queryIdx]
						if field != nil {
							satisfies := [][]string{
								{"User"},
								{"Post"},
								{"Comment"},
								{"Tag"},
								{"User", "Post"},
								{"Post", "Comment"},
								nil,
							}
							_ = CollectFields(
								opCtx,
								field.SelectionSet,
								satisfies[idx%len(satisfies)],
							)
						}
					}(j)
				}

				wg.Wait()
				b.ReportMetric(float64(time.Since(start).Milliseconds()), "wall_clock_ms")
			}
		})

		b.Run(fmt.Sprintf("syncmap_%dg_%dk", cfg.goroutines, cfg.uniqueKeys), func(b *testing.B) {
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				cache := &collectFieldsCacheSyncMap{}
				doc, op := generateComplexQuery(0)
				opCtx := &OperationContext{
					RawQuery:  "bench",
					Variables: make(map[string]any),
					Doc:       doc,
					Operation: op,
				}

				start := time.Now()
				var wg sync.WaitGroup
				wg.Add(cfg.goroutines)

				for j := 0; j < cfg.goroutines; j++ {
					go func(idx int) {
						defer wg.Done()
						queryIdx := idx % cfg.uniqueKeys
						field := queries[queryIdx]
						if field != nil {
							satisfies := [][]string{
								{"User"},
								{"Post"},
								{"Comment"},
								{"Tag"},
								{"User", "Post"},
								{"Post", "Comment"},
								nil,
							}
							_ = CollectFieldsSyncMap(
								cache,
								opCtx,
								field.SelectionSet,
								satisfies[idx%len(satisfies)],
							)
						}
					}(j)
				}

				wg.Wait()
				b.ReportMetric(float64(time.Since(start).Milliseconds()), "wall_clock_ms")
			}
		})
	}
}
