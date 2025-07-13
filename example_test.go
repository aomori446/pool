package pool

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// ExamplePool_basicUsage 展示了 Pool 的基本用法：
// 1. 建立 Pool
// 2. 提交多個任務
// 3. 提交一個關閉信號
// 4. 接收並處理所有結果
func ExamplePool_basicUsage() {
	// 建立一個有 2 個 worker 的 Pool
	pool := NewPool[string](5, 2)

	// 提交 3 個任務
	for i := 0; i < 3; i++ {
		jobID := i
		pool.Submit(Job[string]{
			Execute: func(ctx context.Context) (string, error) {
				// 在真實場景中，這裡會執行一些耗時操作
				return fmt.Sprintf("result from job %d", jobID), nil
			},
		})
	}

	// 所有任務提交完畢後，發送關閉信號
	pool.Submit(NewNoMoreJobsSignal[string]())

	// 等待並收集所有結果
	var results []string
	for result := range pool.Results() {
		if result.Error == nil {
			results = append(results, result.Value)
		}
	}

	// 為了確保輸出穩定，對結果進行排序
	sort.Strings(results)
	for _, r := range results {
		fmt.Println(r)
	}

	// Output:
	// result from job 0
	// result from job 1
	// result from job 2
}

// ExamplePool_cancellation 展示了如何使用 context 來取消一個正在執行的任務。
func ExamplePool_cancellation() {
	pool := NewPool[string](1, 1)
	defer pool.Close()

	// 建立一個 100 毫秒後會超時的 context
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	// 提交一個需要 500 毫秒才能完成的任務，並將可取消的 context 傳入
	pool.Submit(Job[string]{
		Context: ctx,
		Execute: func(ctx context.Context) (string, error) {
			// 這個 select 結構是處理 context 取消的標準模式
			select {
			case <-time.After(500 * time.Millisecond):
				return "job completed", nil
			case <-ctx.Done():
				// Context 被取消 (此處是因為超時)
				return "", ctx.Err()
			}
		},
	})

	// 接收結果
	result := <-pool.Results()
	if result.Error != nil {
		// 檢查錯誤是否為 context.DeadlineExceeded
		if errors.Is(result.Error, context.DeadlineExceeded) {
			fmt.Println("Job successfully cancelled due to timeout.")
		}
	}

	// Output:
	// Job successfully cancelled due to timeout.
}

// ExamplePool_retryWithBackoff 展示了自動重試和指數退避功能。
func ExamplePool_retryWithBackoff() {
	pool := NewPool[string](1, 1)
	defer pool.Close()

	var executionCount int32
	var wg sync.WaitGroup
	wg.Add(1)

	// 提交一個任務，它會失敗 2 次，然後在第 3 次成功
	pool.Submit(Job[string]{
		Execute: func(ctx context.Context) (string, error) {
			atomic.AddInt32(&executionCount, 1)
			if atomic.LoadInt32(&executionCount) < 3 {
				return "", errors.New("temporary failure")
			}
			return "success!", nil
		},
		Retries: 3, // 允許最多重試 3 次
		Backoff: 10 * time.Millisecond,
	})

	go func() {
		defer wg.Done()
		result := <-pool.Results()
		if result.Error == nil {
			fmt.Printf("Job succeeded with value: %s\n", result.Value)
		}
		fmt.Printf("Total execution attempts: %d\n", atomic.LoadInt32(&executionCount))
	}()

	wg.Wait()

	// Output:
	// Job succeeded with value: success!
	// Total execution attempts: 3
}

// ExamplePool_stats 展示了如何獲取 Pool 的運行狀態指標。
func ExamplePool_stats() {
	pool := NewPool[string](5, 2)

	// 提交 5 個任務，其中一些會成功，一些會失敗
	for i := 0; i < 5; i++ {
		jobID := i
		pool.Submit(Job[string]{
			Execute: func(ctx context.Context) (string, error) {
				if jobID%2 == 0 {
					return "success", nil
				}
				return "", errors.New("failed")
			},
			Retries: 1,
		})
	}

	// 等待所有任務被處理
	for pool.Stats().Pending > 0 {
		time.Sleep(10 * time.Millisecond)
	}
	pool.Close() // 關閉 pool 以便 results 通道能被關閉

	// 等待 results 通道被清空
	for range pool.Results() {
	}

	// 獲取最終的統計數據
	stats := pool.Stats()
	var output []string
	output = append(output, fmt.Sprintf("Completed: %d", stats.Completed))
	output = append(output, fmt.Sprintf("Failed: %d", stats.Failed))

	sort.Strings(output)
	for _, s := range output {
		fmt.Println(s)
	}

	// Output:
	// Completed: 3
	// Failed: 2
}
