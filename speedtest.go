package main

import (
    "bufio"
    "crypto/tls"
    "fmt"
    "net"
    "strings"
    "time"
)

// SpeedResult 测速结果
type SpeedResult struct {
    IP            string  `json:"ip"`
    Port          int     `json:"port"`
    LatencyMs     float64 `json:"latencyMs"`
    DownloadSpeed float64 `json:"downloadSpeed"`
    IataCode      string  `json:"iataCode"`
    ChineseName   string  `json:"chineseName"`
    TestType      string  `json:"testType"`
}

// SpeedTester 测速器（串行版本）
type SpeedTester struct {
    AllResults []ScanResult
    RegionCode string
    MaxCount   int
    Port       int
    Running    bool
    OnLog      func(msg string)
    OnProgress func(current, total int)
    OnComplete func(results []SpeedResult)
}

// NewSpeedTester 创建测速器
func NewSpeedTester(allResults []ScanResult, regionCode string, maxCount, port int) *SpeedTester {
    return &SpeedTester{
        AllResults: allResults,
        RegionCode: regionCode,
        MaxCount:   maxCount,
        Port:       port,
        Running:    true,
    }
}

// Stop 停止测速
func (st *SpeedTester) Stop() {
    st.Running = false
}

// Run 执行测速（串行，一个一个测）
func (st *SpeedTester) Run() {
    // 筛选 IP
    var targets []ScanResult
    if st.RegionCode != "" {
        for _, r := range st.AllResults {
            if strings.EqualFold(r.IataCode, st.RegionCode) {
                targets = append(targets, r)
            }
        }
        if len(targets) == 0 {
            st.log(fmt.Sprintf("未找到地区 %s 的 IP", st.RegionCode))
            st.OnComplete([]SpeedResult{})
            return
        }
        st.log(fmt.Sprintf("地区 %s (%s) 共找到 %d 个 IP", st.RegionCode, GetChineseName(st.RegionCode), len(targets)))
    } else {
        targets = st.AllResults
        st.log(fmt.Sprintf("完全测速，共 %d 个 IP", len(targets)))
    }

    // 按延迟排序，取前 MaxCount 个
    sortResultsByLatency(targets)
    if len(targets) > st.MaxCount {
        targets = targets[:st.MaxCount]
    }

    total := len(targets)
    st.log(fmt.Sprintf("将对 %d 个IP进行测速", total))

    speedResults := make([]SpeedResult, 0)

    for idx, target := range targets {
        if !st.Running {
            break
        }
        st.log(fmt.Sprintf("[%d/%d] 正在测速 %s (端口: %d)", idx+1, total, target.IP, st.Port))
        // 测速：下载 50MB，超时 3 秒
        speed := st.downloadSpeedSocket(target.IP, st.Port, 3)
        iata := target.IataCode
        if iata == "" {
            iata = "UNKNOWN"
        }
        chName := GetChineseName(iata)
        sr := SpeedResult{
            IP:            target.IP,
            Port:          st.Port,
            LatencyMs:     target.LatencyMs,
            DownloadSpeed: speed,
            IataCode:      iata,
            ChineseName:   chName,
            TestType: func() string {
                if st.RegionCode != "" {
                    return "地区测速"
                }
                return "完全测速"
            }(),
        }
        speedResults = append(speedResults, sr)
        if st.OnProgress != nil {
            st.OnProgress(idx+1, total)
        }
        st.log(fmt.Sprintf("  测速结果: %.2f MB/s, 地区: %s", speed, chName))
        // 测完一个稍作等待，避免服务端限流
        time.Sleep(300 * time.Millisecond)
    }

    sortResultsBySpeedDesc(speedResults)
    if st.OnComplete != nil {
        st.OnComplete(speedResults)
    }
}

// downloadSpeedSocket 使用原始 socket 测速（硬编码 Cloudflare 默认地址）
func (st *SpeedTester) downloadSpeedSocket(ip string, port int, timeoutSec int) float64 {
    // 构造请求（使用默认的 Cloudflare 测速地址）
    req := fmt.Sprintf("GET /__down?bytes=50000000 HTTP/1.1\r\nHost: speed.cloudflare.com\r\nUser-Agent: Mozilla/5.0\r\nAccept: */*\r\nConnection: close\r\n\r\n")

    var conn net.Conn
    var err error

    // 建立连接（支持 IPv6）
    addr := fmt.Sprintf("%s:%d", ip, port)
    if strings.Contains(ip, ":") {
        addr = fmt.Sprintf("[%s]:%d", ip, port)
    }

    // 尝试 TLS 或普通 TCP
    for _, useTLS := range []bool{port == 443 || port == 8443 || port == 2096, false} {
        if useTLS {
            dialer := &net.Dialer{Timeout: 3 * time.Second}
            conn, err = tls.DialWithDialer(dialer, "tcp", addr, &tls.Config{
                InsecureSkipVerify: true,
                ServerName:         "speed.cloudflare.com",
            })
        } else {
            conn, err = net.DialTimeout("tcp", addr, 3*time.Second)
        }
        if err == nil {
            break
        }
    }
    if err != nil {
        return 0
    }
    defer conn.Close()

    // 发送请求
    _, err = conn.Write([]byte(req))
    if err != nil {
        return 0
    }

    // 设置读取超时（整个测速过程不超过 timeoutSec+1 秒）
    conn.SetReadDeadline(time.Now().Add(time.Duration(timeoutSec+1) * time.Second))

    // 读取响应头
    reader := bufio.NewReader(conn)
    // 跳过所有头部直到空行
    for {
        line, err := reader.ReadString('\n')
        if err != nil {
            return 0
        }
        if line == "\r\n" || line == "\n" {
            break
        }
    }

    // 开始读取 body 并计时
    start := time.Now()
    var downloaded int64
    buf := make([]byte, 8192) // 8KB buffer

    for {
        // 检查是否超时
        if time.Since(start).Seconds() > float64(timeoutSec) {
            break
        }
        n, err := reader.Read(buf)
        if n > 0 {
            downloaded += int64(n)
        }
        if err != nil {
            break
        }
    }
    elapsed := time.Since(start).Seconds()
    if elapsed < 0.1 {
        elapsed = 0.1
    }
    speed := float64(downloaded) / (1024 * 1024) / elapsed
    return speed
}

func (st *SpeedTester) log(msg string) {
    if st.OnLog != nil {
        st.OnLog(msg)
    }
}

// 辅助排序函数
func sortResultsByLatency(results []ScanResult) {
    for i := 0; i < len(results)-1; i++ {
        for j := i + 1; j < len(results); j++ {
            if results[i].LatencyMs > results[j].LatencyMs {
                results[i], results[j] = results[j], results[i]
            }
        }
    }
}

func sortResultsBySpeedDesc(results []SpeedResult) {
    for i := 0; i < len(results)-1; i++ {
        for j := i + 1; j < len(results); j++ {
            if results[i].DownloadSpeed < results[j].DownloadSpeed {
                results[i], results[j] = results[j], results[i]
            }
        }
    }
}