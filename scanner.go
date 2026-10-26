package main

import (
    "bufio"
    "crypto/tls"
    "fmt"
    "net"
    "strings"
    "sync"
    "time"
)

type ScanResult struct {
    IP          string  `json:"ip"`
    Port        int     `json:"port"`
    LatencyMs   float64 `json:"latencyMs"`
    IataCode    string  `json:"iataCode"`
    ChineseName string  `json:"chineseName"`
    Success     bool    `json:"success"`
}

type Scanner struct {
    Version          int
    Port             int
    MaxWorkers       int
    LatencyThreshold int
    V4Sample         int
    V6Sample         int
    Running          bool
    OnLog            func(msg string)
    OnProgress       func(current, total, found int, speed float64)
    OnResult         func(result ScanResult)
}

// NewScanner 接收6个参数
func NewScanner(version, port, maxWorkers, latencyThreshold, v4Sample, v6Sample int) *Scanner {
    return &Scanner{
        Version:          version,
        Port:             port,
        MaxWorkers:       maxWorkers,
        LatencyThreshold: latencyThreshold,
        V4Sample:         v4Sample,
        V6Sample:         v6Sample,
        Running:          true,
    }
}

func (s *Scanner) Stop() {
    s.Running = false
}

func (s *Scanner) Run() []ScanResult {
    s.log(fmt.Sprintf("正在从Cloudflare IPv%d IP段生成随机IP... (端口: %d)", s.Version, s.Port))
    ips := s.generateIPs()
    if len(ips) == 0 {
        s.log(fmt.Sprintf("错误: 未能生成IPv%d IP列表", s.Version))
        return nil
    }
    s.log(fmt.Sprintf("已生成 %d 个随机IPv%d IP", len(ips), s.Version))
    s.log(fmt.Sprintf("开始延迟测试 %d 个IPv%d IP...", len(ips), s.Version))

    results := make([]ScanResult, 0)
    var mu sync.Mutex
    var wg sync.WaitGroup
    sem := make(chan struct{}, s.MaxWorkers)

    total := len(ips)
    completed := 0
    found := 0
    startTime := time.Now()

    for _, ip := range ips {
        if !s.Running {
            break
        }
        wg.Add(1)
        go func(ipAddr string) {
            defer wg.Done()
            sem <- struct{}{}
            defer func() { <-sem }()

            if !s.Running {
                return
            }

            latency := s.tcpPing(ipAddr, s.Port, 1*time.Second)
            if latency == 0 || latency > float64(s.LatencyThreshold) {
                mu.Lock()
                completed++
                s.updateProgress(completed, total, found, startTime)
                mu.Unlock()
                return
            }

            iata := s.getIataCodeSocket(ipAddr, s.Port)
            chName := GetChineseName(iata)
            if iata == "" {
                iata = "UNKNOWN"
                chName = "未知地区"
            }

            result := ScanResult{
                IP:          ipAddr,
                Port:        s.Port,
                LatencyMs:   latency,
                IataCode:    iata,
                ChineseName: chName,
                Success:     true,
            }
            mu.Lock()
            results = append(results, result)
            found++
            completed++
            s.updateProgress(completed, total, found, startTime)
            if s.OnResult != nil {
                s.OnResult(result)
            }
            mu.Unlock()
        }(ip)
    }
    wg.Wait()
    s.log(fmt.Sprintf("扫描结束，共发现 %d 个可用IP", len(results)))
    return results
}

// generateIPs 传递采样参数
func (s *Scanner) generateIPs() []string {
    if s.Version == 4 {
        return generateIPv4List(s.V4Sample)
    }
    return generateIPv6List(s.V6Sample)
}

func (s *Scanner) tcpPing(ip string, port int, timeout time.Duration) float64 {
    start := time.Now()
    addr := fmt.Sprintf("%s:%d", ip, port)
    if strings.Contains(ip, ":") {
        addr = fmt.Sprintf("[%s]:%d", ip, port)
    }
    conn, err := net.DialTimeout("tcp", addr, timeout)
    if err != nil {
        return 0
    }
    defer conn.Close()
    return float64(time.Since(start).Microseconds()) / 1000.0
}

func (s *Scanner) getIataCodeSocket(ip string, port int) string {
    for _, useTLS := range []bool{true, false} {
        addr := fmt.Sprintf("%s:%d", ip, port)
        if strings.Contains(ip, ":") {
            addr = fmt.Sprintf("[%s]:%d", ip, port)
        }
        var conn net.Conn
        var err error
        if useTLS {
            dialer := &net.Dialer{Timeout: 2 * time.Second}
            conn, err = tls.DialWithDialer(dialer, "tcp", addr, &tls.Config{
                InsecureSkipVerify: true,
                ServerName:         "speed.cloudflare.com",
            })
        } else {
            conn, err = net.DialTimeout("tcp", addr, 2*time.Second)
        }
        if err != nil {
            continue
        }
        req := fmt.Sprintf("GET /cdn-cgi/trace HTTP/1.1\r\nHost: speed.cloudflare.com\r\nUser-Agent: Mozilla/5.0\r\nConnection: close\r\n\r\n")
        _, err = conn.Write([]byte(req))
        if err != nil {
            conn.Close()
            continue
        }
        conn.SetReadDeadline(time.Now().Add(3 * time.Second))
        reader := bufio.NewReader(conn)
        for {
            line, err := reader.ReadString('\n')
            if err != nil {
                break
            }
            if line == "\r\n" || line == "\n" {
                break
            }
        }
        body := ""
        for {
            line, err := reader.ReadString('\n')
            if err != nil {
                break
            }
            body += line
        }
        conn.Close()
        lines := strings.Split(body, "\n")
        for _, line := range lines {
            if strings.HasPrefix(line, "colo=") {
                code := strings.TrimSpace(strings.TrimPrefix(line, "colo="))
                if code != "" && code != "UNKNOWN" {
                    return strings.ToUpper(code)
                }
            }
        }
    }
    return ""
}

func (s *Scanner) updateProgress(current, total, found int, startTime time.Time) {
    if s.OnProgress == nil {
        return
    }
    elapsed := time.Since(startTime).Seconds()
    speed := float64(current) / elapsed
    s.OnProgress(current, total, found, speed)
}

func (s *Scanner) log(msg string) {
    if s.OnLog != nil {
        s.OnLog(msg)
    }
}