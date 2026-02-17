#!/bin/bash

# Script to run and compare collectFieldsCache benchmarks
# Demonstrates the lock contention issue and sync.Map improvement

set -e

echo "=========================================="
echo "CollectFields Cache Benchmark"
echo "Mutex vs sync.Map Implementation"
echo "=========================================="
echo ""

echo "Testing cold start contention (the production issue)..."
echo "This simulates multiple goroutines hitting an empty cache simultaneously"
echo ""

# Run cold start benchmarks
go test -run=^$ \
  -bench="BenchmarkCollectFieldsCache_ColdStart_Concurrent$|BenchmarkCollectFieldsCache_SyncMap_ColdStart_Concurrent$" \
  -benchtime=3s \
  -benchmem \
  -cpu=8 \
  | tee /tmp/coldstart_results.txt

echo ""
echo "=========================================="
echo "Testing warm cache (already populated)..."
echo "This shows performance when cache hits are common"
echo ""

# Run warm cache benchmarks
go test -run=^$ \
  -bench="BenchmarkCollectFieldsCache_WarmCache_Concurrent$|BenchmarkCollectFieldsCache_SyncMap_WarmCache_Concurrent$" \
  -benchtime=3s \
  -benchmem \
  -cpu=8 \
  | tee /tmp/warmcache_results.txt

echo ""
echo "=========================================="
echo "Summary"
echo "=========================================="
echo ""
echo "Cold start results show the lock contention issue."
echo "Look for:"
echo "  - sync.Map should have lower ns/op (faster)"
echo "  - Performance gap increases with more goroutines"
echo "  - sync.Map allocations may be slightly higher but speed is better"
echo ""
echo "To analyze results, look for patterns like:"
echo "  200_goroutines: Mutex=50000ns, SyncMap=45000ns → 10% faster"
echo ""
echo "Results saved to:"
echo "  /tmp/coldstart_results.txt"
echo "  /tmp/warmcache_results.txt"
