package graphql

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/vektah/gqlparser/v2/ast"
)

// This test simulates the 700ms scenario by artificially making collectFields slower
// to match what might happen with very complex production queries

// Simulate a slow collectFields operation (complex query processing)
func slowCollectFields(
	reqCtx *OperationContext,
	selSet ast.SelectionSet,
	satisfies []string,
	computeDelay time.Duration,
) {
	cacheKey := makeCollectFieldsCacheKey(selSet, satisfies)

	if cached, ok := reqCtx.collectFieldsCache.Get(cacheKey); ok {
		_ = cached
		return
	}

	// Simulate complex query processing that takes time
	time.Sleep(computeDelay)

	result := collectFields(reqCtx, selSet, satisfies, map[string]bool{}, false)
	_ = reqCtx.collectFieldsCache.Add(cacheKey, result)
}

func slowCollectFieldsSyncMap(
	cache *collectFieldsCacheSyncMap,
	reqCtx *OperationContext,
	selSet ast.SelectionSet,
	satisfies []string,
	computeDelay time.Duration,
) {
	cacheKey := makeCollectFieldsCacheKey(selSet, satisfies)

	if cached, ok := cache.Get(cacheKey); ok {
		_ = cached
		return
	}

	// Simulate complex query processing
	time.Sleep(computeDelay)

	result := collectFields(reqCtx, selSet, satisfies, map[string]bool{}, false)
	_ = cache.Add(cacheKey, result)
}

// Test with artificial slowdown to see if we can hit 700ms
func TestCollectFieldsCache_SlowQuery_Contention(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping slow query contention test in short mode")
	}

	tests := []struct {
		name              string
		numGoroutines     int
		queryComplexityMs int // How long each collectFields computation takes
		targetTimeMs      int
	}{
		{"moderate_slow_query", 100, 5, 100}, // 100 goroutines, 5ms per query
		{"complex_slow_query", 200, 5, 300},  // 200 goroutines, 5ms per query
		{"very_complex_query", 500, 2, 700},  // 500 goroutines, 2ms per query
		{"extreme_complexity", 1000, 1, 700}, // 1000 goroutines, 1ms per query
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Logf("Testing with %d goroutines, %dms query complexity (targeting ~%dms)",
				tt.numGoroutines, tt.queryComplexityMs, tt.targetTimeMs)

			queryDelay := time.Duration(tt.queryComplexityMs) * time.Millisecond

			// Test mutex implementation
			doc, op := generateComplexQuery(0)
			opCtx := &OperationContext{
				RawQuery:  "slow query test",
				Variables: make(map[string]any),
				Doc:       doc,
				Operation: op,
			}

			var field *ast.Field
			if len(op.SelectionSet) > 0 {
				field, _ = op.SelectionSet[0].(*ast.Field)
			}
			if field == nil {
				t.Skip("No field found in query")
			}

			start := time.Now()
			var wg sync.WaitGroup
			wg.Add(tt.numGoroutines)

			for j := 0; j < tt.numGoroutines; j++ {
				go func(idx int) {
					defer wg.Done()
					// Use different satisfies to create contention on writes
					satisfies := []string{"User", "Post", "Comment", "Tag"}
					satisfy := []string{satisfies[idx%len(satisfies)]}
					slowCollectFields(opCtx, field.SelectionSet, satisfy, queryDelay)
				}(j)
			}

			wg.Wait()
			mutexTime := time.Since(start)

			// Test sync.Map implementation
			cache := &collectFieldsCacheSyncMap{}
			opCtxSync := &OperationContext{
				RawQuery:  "slow query test",
				Variables: make(map[string]any),
				Doc:       doc,
				Operation: op,
			}

			start = time.Now()
			wg.Add(tt.numGoroutines)

			for j := 0; j < tt.numGoroutines; j++ {
				go func(idx int) {
					defer wg.Done()
					satisfies := []string{"User", "Post", "Comment", "Tag"}
					satisfy := []string{satisfies[idx%len(satisfies)]}
					slowCollectFieldsSyncMap(
						cache,
						opCtxSync,
						field.SelectionSet,
						satisfy,
						queryDelay,
					)
				}(j)
			}

			wg.Wait()
			syncMapTime := time.Since(start)

			improvement := float64(mutexTime-syncMapTime) / float64(mutexTime) * 100
			savedTime := mutexTime - syncMapTime

			t.Logf("Mutex implementation:   %v", mutexTime)
			t.Logf("sync.Map implementation: %v", syncMapTime)
			t.Logf("Improvement: %.1f%% faster (saved %v)", improvement, savedTime)

			if mutexTime > time.Duration(tt.targetTimeMs)*time.Millisecond {
				t.Logf(
					"✓ Successfully reproduced production-level contention (%dms+)",
					tt.targetTimeMs,
				)
				t.Logf("✓ sync.Map saved %v of contention time", savedTime)
			}

			// Report what the improvement means in production
			if savedTime > 100*time.Millisecond {
				t.Logf("🎯 In production, this would save %v per burst of %d requests",
					savedTime, tt.numGoroutines)
			}
		})
	}
}

// Benchmark the slow query scenario
func BenchmarkCollectFieldsCache_SlowQuery(b *testing.B) {
	complexityMs := []int{1, 2, 5} // Different query complexities
	goroutines := []int{100, 500, 1000}

	for _, complexity := range complexityMs {
		for _, numGoroutines := range goroutines {
			b.Run(fmt.Sprintf("mutex_%dms_%dg", complexity, numGoroutines), func(b *testing.B) {
				queryDelay := time.Duration(complexity) * time.Millisecond
				doc, op := generateComplexQuery(0)
				var field *ast.Field
				if len(op.SelectionSet) > 0 {
					field, _ = op.SelectionSet[0].(*ast.Field)
				}

				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					opCtx := &OperationContext{
						RawQuery:  "bench",
						Variables: make(map[string]any),
						Doc:       doc,
						Operation: op,
					}

					start := time.Now()
					var wg sync.WaitGroup
					wg.Add(numGoroutines)

					for j := 0; j < numGoroutines; j++ {
						go func(idx int) {
							defer wg.Done()
							satisfies := []string{"User", "Post", "Comment", "Tag"}
							satisfy := []string{satisfies[idx%len(satisfies)]}
							slowCollectFields(opCtx, field.SelectionSet, satisfy, queryDelay)
						}(j)
					}

					wg.Wait()
					elapsed := time.Since(start)
					b.ReportMetric(float64(elapsed.Milliseconds()), "wall_clock_ms")
				}
			})

			b.Run(fmt.Sprintf("syncmap_%dms_%dg", complexity, numGoroutines), func(b *testing.B) {
				queryDelay := time.Duration(complexity) * time.Millisecond
				doc, op := generateComplexQuery(0)
				var field *ast.Field
				if len(op.SelectionSet) > 0 {
					field, _ = op.SelectionSet[0].(*ast.Field)
				}

				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					cache := &collectFieldsCacheSyncMap{}
					opCtx := &OperationContext{
						RawQuery:  "bench",
						Variables: make(map[string]any),
						Doc:       doc,
						Operation: op,
					}

					start := time.Now()
					var wg sync.WaitGroup
					wg.Add(numGoroutines)

					for j := 0; j < numGoroutines; j++ {
						go func(idx int) {
							defer wg.Done()
							satisfies := []string{"User", "Post", "Comment", "Tag"}
							satisfy := []string{satisfies[idx%len(satisfies)]}
							slowCollectFieldsSyncMap(
								cache,
								opCtx,
								field.SelectionSet,
								satisfy,
								queryDelay,
							)
						}(j)
					}

					wg.Wait()
					elapsed := time.Since(start)
					b.ReportMetric(float64(elapsed.Milliseconds()), "wall_clock_ms")
				}
			})
		}
	}
}
