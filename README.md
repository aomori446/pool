# Generic Worker Pool (Go)

本套件提供一個 **高可擴充性、支援泛型、具備重試與超時控制** 的 Golang Worker Pool 實作，適用於各種需要併發處理任務的應用場景，例如：

- 批次資料處理
- API 呼叫控制
- DNS 掃描與測試
- 自訂重試任務機制

## ✨ 功能特色

- ✅ 支援泛型任務回傳型別 (`Job[R]`)
- ✅ 支援 Context 管理與逾時 (`timeout`)
- ✅ 支援任務重試 (`Retries`) 與指數退避 (`Backoff`)
- ✅ 支援優雅關閉 (`Drain`) 與強制中止 (`ShutdownNow`)
- ✅ 可動態調整 worker 數量 (`Resize`)
- ✅ 提供執行結果與統計數據 (`Results`, `Stats`)

---

## 📦 安裝方式

使用 `go get` 取得：

```bash
go get github.com/aomori168/pool@latest
