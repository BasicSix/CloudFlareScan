package main

import (
    "context"
    "encoding/csv"
    "fmt"
    "os"
    "strconv"
    "strings"
    "time"

    "github.com/wailsapp/wails/v2/pkg/runtime"
    "golang.org/x/text/encoding/simplifiedchinese"
    "golang.org/x/text/transform"
)

type App struct {
    ctx               context.Context
    currentScanner    *Scanner
    currentSpeedTester *SpeedTester
    scanning          bool
    speedTesting      bool
    lastScanResults   []ScanResult
}

func NewApp() *App {
    return &App{}
}

func (a *App) startup(ctx context.Context) {
    a.ctx = ctx
    a.scanning = false
    a.speedTesting = false
}

// StartScan 增加 v4Sample, v6Sample 参数
func (a *App) StartScan(version int, port int, maxWorkers int, latencyThreshold int, v4Sample int, v6Sample int) {
    if a.scanning || a.speedTesting {
        a.EmitLog("已有任务正在执行，请先停止")
        return
    }
    a.scanning = true
    a.EmitLog(fmt.Sprintf("开始 IPv%d 扫描，端口: %d，并发: %d，延迟上限: %d ms，V4采样: %d，V6采样: %d",
        version, port, maxWorkers, latencyThreshold, v4Sample, v6Sample))

    scanner := NewScanner(version, port, maxWorkers, latencyThreshold, v4Sample, v6Sample)
    a.currentScanner = scanner

    scanner.OnLog = func(msg string) {
        a.EmitLog(msg)
    }
    scanner.OnProgress = func(current, total, found int, speed float64) {
        runtime.EventsEmit(a.ctx, "scan-progress", current, total, found, speed)
    }
    scanner.OnResult = func(result ScanResult) {
        runtime.EventsEmit(a.ctx, "scan-result", result)
    }

    go func() {
        results := scanner.Run()
        a.lastScanResults = results
        a.scanning = false
        a.currentScanner = nil
        runtime.EventsEmit(a.ctx, "scan-completed", results)
        a.EmitLog(fmt.Sprintf("扫描完成，共发现 %d 个可用 IP", len(results)))
    }()
}

func (a *App) StopScan() {
    if a.currentScanner != nil {
        a.currentScanner.Stop()
        a.EmitLog("正在停止扫描...")
    }
}

func (a *App) StartFullSpeedTest(maxCount int, port int) {
    if a.speedTesting || a.scanning {
        a.EmitLog("请先停止当前任务")
        return
    }
    if len(a.lastScanResults) == 0 {
        a.EmitLog("没有可用的 IP，请先进行扫描")
        return
    }
    a.speedTesting = true
    a.EmitLog(fmt.Sprintf("开始完全测速，测速数量: %d，端口: %d", maxCount, port))

    tester := NewSpeedTester(a.lastScanResults, "", maxCount, port)
    a.currentSpeedTester = tester
    tester.OnLog = func(msg string) {
        a.EmitLog(msg)
    }
    tester.OnProgress = func(current, total int) {
        runtime.EventsEmit(a.ctx, "speed-progress", current, total)
    }
    tester.OnComplete = func(results []SpeedResult) {
        a.speedTesting = false
        a.currentSpeedTester = nil
        runtime.EventsEmit(a.ctx, "speed-completed", results)
        a.EmitLog(fmt.Sprintf("测速完成，成功测速 %d 个 IP", len(results)))
    }

    go tester.Run()
}

func (a *App) StartRegionSpeedTest(regionCode string, maxCount int, port int) {
    if a.speedTesting || a.scanning {
        a.EmitLog("请先停止当前任务")
        return
    }
    if len(a.lastScanResults) == 0 {
        a.EmitLog("没有可用的 IP，请先进行扫描")
        return
    }
    regionCode = strings.ToUpper(regionCode)
    a.speedTesting = true
    a.EmitLog(fmt.Sprintf("开始地区测速: %s (%s)，测速数量: %d，端口: %d",
        regionCode, GetChineseName(regionCode), maxCount, port))

    tester := NewSpeedTester(a.lastScanResults, regionCode, maxCount, port)
    a.currentSpeedTester = tester
    tester.OnLog = func(msg string) {
        a.EmitLog(msg)
    }
    tester.OnProgress = func(current, total int) {
        runtime.EventsEmit(a.ctx, "speed-progress", current, total)
    }
    tester.OnComplete = func(results []SpeedResult) {
        a.speedTesting = false
        a.currentSpeedTester = nil
        runtime.EventsEmit(a.ctx, "speed-completed", results)
        a.EmitLog(fmt.Sprintf("地区测速完成，成功测速 %d 个 IP", len(results)))
    }

    go tester.Run()
}

func (a *App) StopSpeedTest() {
    if a.currentSpeedTester != nil {
        a.currentSpeedTester.Stop()
        a.EmitLog("正在停止测速...")
    }
}

// ExportResults 导出测速结果（mode: "csv" 或 "cm"）
func (a *App) ExportResults(results []SpeedResult, mode string) (string, error) {
    if len(results) == 0 {
        return "", fmt.Errorf("没有测速结果可导出")
    }

    // 根据模式决定默认文件名与过滤器
    if mode == "cm" {
        return a.exportCM(results)
    }
    // 默认 CSV
    return a.exportCSV(results)
}

// exportCSV 导出为 CSV（GBK 编码，Excel 打开中文不乱码）
func (a *App) exportCSV(results []SpeedResult) (string, error) {
    savePath, err := runtime.SaveFileDialog(a.ctx, runtime.SaveDialogOptions{
        DefaultDirectory: ".",
        DefaultFilename:  fmt.Sprintf("cfs_results_%s.csv", time.Now().Format("20060102")),
        Title:            "保存测速结果 (CSV)",
        Filters: []runtime.FileFilter{
            {DisplayName: "CSV文件", Pattern: "*.csv"},
        },
    })
    if err != nil {
        return "", err
    }
    if savePath == "" {
        return "", nil
    }

    // 创建 GBK 编码器
    encoder := simplifiedchinese.GBK.NewEncoder()
    file, err := os.Create(savePath)
    if err != nil {
        return "", err
    }
    defer file.Close()

    writer := csv.NewWriter(transform.NewWriter(file, encoder))
    writer.UseCRLF = true // Windows 换行符
    defer writer.Flush()

    headers := []string{"排名", "IP地址", "地区码", "地区", "延迟(ms)", "下载速度(MB/s)", "端口", "测速类型"}
    if err := writer.Write(headers); err != nil {
        return "", err
    }

    for i, r := range results {
        row := []string{
            strconv.Itoa(i + 1),
            r.IP,
            r.IataCode,
            r.ChineseName,
            fmt.Sprintf("%.2f", r.LatencyMs),
            fmt.Sprintf("%.2f", r.DownloadSpeed),
            strconv.Itoa(r.Port),
            r.TestType,
        }
        if err := writer.Write(row); err != nil {
            return "", err
        }
    }

    writer.Flush()
    if err := writer.Error(); err != nil {
        return "", err
    }

    return savePath, nil
}

// exportCM 导出为 CM 自定义格式（每行一条，UTF-8 无 BOM）
// IPv4:  104.24.0.232:8443#SIN 新加坡 (76ms  23MB/s)
// IPv6:  [2606:4700::]:2053#NRT 日本 (36ms  53MB/s)
func (a *App) exportCM(results []SpeedResult) (string, error) {
    savePath, err := runtime.SaveFileDialog(a.ctx, runtime.SaveDialogOptions{
        DefaultDirectory: ".",
        DefaultFilename:  fmt.Sprintf("cfs_cm_%s.txt", time.Now().Format("20060102")),
        Title:            "保存测速结果 (CM自定义格式)",
        Filters: []runtime.FileFilter{
            {DisplayName: "文本文件", Pattern: "*.txt"},
        },
    })
    if err != nil {
        return "", err
    }
    if savePath == "" {
        return "", nil
    }

    file, err := os.Create(savePath)
    if err != nil {
        return "", err
    }
    defer file.Close()

    for _, r := range results {
        // 地区码为空或 UNKNOWN 时用 UNKNOWN 占位
        code := r.IataCode
        if code == "" {
            code = "UNKNOWN"
        }
        name := r.ChineseName
        if name == "" {
            name = "未知地区"
        }

        var line string
        if strings.Contains(r.IP, ":") {
            // IPv6：ip 用方括号包裹
            line = fmt.Sprintf("[%s]:%d#%s %s (%.0fms  %.0fMB/s)\n",
                r.IP, r.Port, code, name, r.LatencyMs, r.DownloadSpeed)
        } else {
            // IPv4
            line = fmt.Sprintf("%s:%d#%s %s (%.0fms  %.0fMB/s)\n",
                r.IP, r.Port, code, name, r.LatencyMs, r.DownloadSpeed)
        }

        if _, err := file.WriteString(line); err != nil {
            return "", err
        }
    }

    return savePath, nil
}

func (a *App) EmitLog(msg string) {
    runtime.EventsEmit(a.ctx, "log-message", msg)
}

func (a *App) GetChineseName(iataCode string) string {
    return GetChineseName(iataCode)
}