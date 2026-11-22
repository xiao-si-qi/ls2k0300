// switchyard-agent —— LS2K0300 的 Switchyard 设备代理（Go 版）
//
// 按 switchyard/docs/PROTOCOL.md v0.1 实现，和之前的 Python 版行为一致：
//
//	发布  switchyard/<id>/state         retained   每 INTERVAL 秒
//	      switchyard/<id>/availability  retained   online/offline（LWT）
//	      switchyard/<id>/meta          retained   连接成功时
//	      switchyard/<id>/event         非 retained boot / usb_attached / usb_detached / cfg_applied
//	      switchyard/<id>/diag          retained   错误摘要，限流 1 条/分钟
//	      switchyard/<id>/log           非 retained 按需回传日志
//	      switchyard/<id>/ack           非 retained 命令确认
//	订阅  switchyard/<id>/cmd           命令（seq 去重）
//	      switchyard/<id>/cfg           配置（retained，落盘）
//
// 目标平台：linux/loong64（Go 的 loong64 后端不使用 LSX/LASX 向量指令，
// 所以在这颗没有向量单元的 LA264 上不会像 Rust 预编译产物那样 SIGILL）
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

// ---------------------------------------------------------------- 配置

var (
	deviceID   = env("SY_DEVICE_ID", "ls2k0300")
	deviceName = env("SY_DEVICE_NAME", "LS2K0300 板子")
	model      = "Loongson 2K0300"
	devType    = "host"
	caps       = []string{"fw", "rssi", "uptime", "online"}

	host = env("MQTT_HOST", "localhost")
	port = envInt("MQTT_PORT", 1883)
	user = env("MQTT_USERNAME", "")
	pass = env("MQTT_PASSWORD", "")

	intervalMin = 15
	intervalMax = 3600
)

const (
	stateDir  = "/var/lib/switchyard-agent"
	cfgFile   = stateDir + "/cfg.json"
	seqFile   = stateDir + "/seq.json"
	agentLog  = "/var/log/switchyard-agent.log"
	logTailN  = 40
	diagGap   = 60 * time.Second
)

var (
	tState = "switchyard/" + deviceID + "/state"
	tAvail = "switchyard/" + deviceID + "/availability"
	tMeta  = "switchyard/" + deviceID + "/meta"
	tEvent = "switchyard/" + deviceID + "/event"
	tDiag  = "switchyard/" + deviceID + "/diag"
	tLog   = "switchyard/" + deviceID + "/log"
	tAck   = "switchyard/" + deviceID + "/ack"
	tCmd   = "switchyard/" + deviceID + "/cmd"
	tCfg   = "switchyard/" + deviceID + "/cfg"
)

var (
	client   mqtt.Client
	interval atomic.Int64 // 秒
	lastSeq  atomic.Int64

	stateMu     sync.Mutex
	cpuLoad     atomic.Value // float64
	lastDiagAt  atomic.Int64 // unix nano
)

// ---------------------------------------------------------------- 数据结构

type StatePayload struct {
	ID         string   `json:"id"`
	Type       string   `json:"type"`
	FW         string   `json:"fw"`
	IP         *string  `json:"ip"`
	RSSI       *int     `json:"rssi"`
	Uptime     int64    `json:"uptime"`
	Heap       int64    `json:"heap"`
	Battery    *int     `json:"battery"`
	TS         int64    `json:"ts"`
	CPULoad    *float64 `json:"cpu_load"`
	MemTotalKB int64    `json:"mem_total_kb"`
	MemFreeKB  int64    `json:"mem_free_kb"`
	MemUsedPct *float64 `json:"mem_used_pct"`
	DiskFreeMB *int64   `json:"disk_free_mb"`
	USB        bool     `json:"usb"`
	Interval   int64    `json:"interval"`
}

type MetaPayload struct {
	Type     string   `json:"type"`
	Caps     []string `json:"caps"`
	Name     string   `json:"name"`
	Model    string   `json:"model"`
	Interval int64    `json:"interval"`
}

type EventPayload struct {
	Ev   string `json:"ev"`
	TS   int64  `json:"ts"`
	FW   string `json:"fw,omitempty"`
	Info string `json:"info,omitempty"`
}

type AckPayload struct {
	Seq int64  `json:"seq"`
	OK  bool   `json:"ok"`
	Err string `json:"err,omitempty"`
}

type Command struct {
	Seq   *int64          `json:"seq"`
	Cmd   string          `json:"cmd"`
	Key   string          `json:"key"`
	Value json.RawMessage `json:"value"`
}

// ---------------------------------------------------------------- 小工具

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func logf(format string, args ...any) {
	fmt.Printf(format+"\n", args...)
}

func readFile(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func clampInterval(v int64) int64 {
	if v < int64(intervalMin) {
		return int64(intervalMin)
	}
	if v > int64(intervalMax) {
		return int64(intervalMax)
	}
	return v
}

// ---------------------------------------------------------------- 采集

func kernelVersion() string {
	if v := readFile("/proc/sys/kernel/osrelease"); v != "" {
		return v
	}
	return "unknown"
}

func uptimeSeconds() int64 {
	fields := strings.Fields(readFile("/proc/uptime"))
	if len(fields) == 0 {
		return 0
	}
	f, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0
	}
	return int64(f)
}

func procStat() (total, idle uint64, ok bool) {
	line := strings.SplitN(readFile("/proc/stat"), "\n", 2)[0]
	fields := strings.Fields(line)
	if len(fields) < 5 || fields[0] != "cpu" {
		return 0, 0, false
	}
	var vals []uint64
	for _, f := range fields[1:] {
		n, err := strconv.ParseUint(f, 10, 64)
		if err != nil {
			break
		}
		vals = append(vals, n)
	}
	if len(vals) < 4 {
		return 0, 0, false
	}
	var sum uint64
	for _, v := range vals {
		sum += v
	}
	idle = vals[3]
	if len(vals) > 4 {
		idle += vals[4] // iowait 也算空闲
	}
	return sum, idle, true
}

// cpuSampler 每秒采一次 /proc/stat，算 CPU 占用率（和 Python 版行为一致）。
func cpuSampler() {
	for {
		t1, i1, ok1 := procStat()
		time.Sleep(time.Second)
		t2, i2, ok2 := procStat()
		if !ok1 || !ok2 || t2 <= t1 {
			continue
		}
		dt := float64(t2 - t1)
		di := float64(i2 - i1)
		pct := 100.0 * (dt - di) / dt
		if pct < 0 {
			pct = 0
		}
		cpuLoad.Store(pct)
		time.Sleep(9 * time.Second)
	}
}

func wlanRSSI() *int {
	for _, line := range strings.Split(readFile("/proc/net/wireless"), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 || !strings.HasPrefix(fields[0], "wlan") {
			continue
		}
		q, err := strconv.ParseFloat(strings.TrimRight(fields[2], "."), 64)
		if err != nil {
			continue
		}
		v := int(q/2 - 100)
		return &v
	}
	return nil
}

var ipRe = regexp.MustCompile(`^\d+:\s+(\S+)\s+inet\s+(\d+\.\d+\.\d+\.\d+)`)

func primaryIP() *string {
	out, err := exec.Command("ip", "-4", "-o", "addr", "show", "scope", "global").Output()
	if err != nil {
		return nil
	}
	var fallback *string
	for _, line := range strings.Split(string(out), "\n") {
		m := ipRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		addr := m[2]
		if strings.HasPrefix(m[1], "eth") {
			return &addr
		}
		if fallback == nil {
			fallback = &addr
		}
	}
	return fallback
}

func memInfo() (totalKB, availKB int64) {
	for _, line := range strings.Split(readFile("/proc/meminfo"), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		n, _ := strconv.ParseInt(fields[1], 10, 64)
		switch fields[0] {
		case "MemTotal:":
			totalKB = n
		case "MemAvailable:":
			availKB = n
		}
	}
	return
}

func diskFreeMB() *int64 {
	var st syscall.Statfs_t
	if err := syscall.Statfs("/", &st); err != nil {
		return nil
	}
	v := int64(st.Bavail) * int64(st.Frsize) / 1048576
	return &v
}

func usbPresent() bool {
	_, err := os.Stat("/dev/sda")
	return err == nil
}

func buildState() StatePayload {
	totalKB, availKB := memInfo()
	var usedPct *float64
	if totalKB > 0 {
		p := float64(totalKB-availKB) / float64(totalKB) * 100
		p = float64(int(p*10)) / 10
		usedPct = &p
	}
	var load *float64
	if v, ok := cpuLoad.Load().(float64); ok {
		load = &v
	}
	return StatePayload{
		ID:         deviceID,
		Type:       devType,
		FW:         kernelVersion(),
		IP:         primaryIP(),
		RSSI:       wlanRSSI(),
		Uptime:     uptimeSeconds(),
		Heap:       availKB,
		TS:         time.Now().Unix(),
		CPULoad:    load,
		MemTotalKB: totalKB,
		MemFreeKB:  availKB,
		MemUsedPct: usedPct,
		DiskFreeMB: diskFreeMB(),
		USB:        usbPresent(),
		Interval:   interval.Load(),
	}
}

func buildMeta() MetaPayload {
	return MetaPayload{
		Type:     devType,
		Caps:     caps,
		Name:     deviceName,
		Model:    model,
		Interval: interval.Load(),
	}
}

// ---------------------------------------------------------------- 发布

func publish(topic string, v any, retain bool) bool {
	if client == nil || !client.IsConnectionOpen() {
		return false
	}
	var payload []byte
	switch t := v.(type) {
	case string:
		payload = []byte(t)
	case []byte:
		payload = t
	default:
		b, err := json.Marshal(t)
		if err != nil {
			return false
		}
		payload = b
	}
	tok := client.Publish(topic, 1, retain, payload)
	tok.WaitTimeout(5 * time.Second)
	return tok.Error() == nil
}

func publishState() bool  { return publish(tState, buildState(), true) }
func publishMeta() bool   { return publish(tMeta, buildMeta(), true) }

func publishEvent(ev string, extra ...string) bool {
	e := EventPayload{Ev: ev, TS: time.Now().Unix()}
	for i := 0; i+1 < len(extra); i += 2 {
		switch extra[i] {
		case "fw":
			e.FW = extra[i+1]
		case "info":
			e.Info = extra[i+1]
		}
	}
	logf("[event] %s", ev)
	return publish(tEvent, e, false)
}

func publishDiag(text string) bool {
	now := time.Now()
	last := time.Unix(0, lastDiagAt.Load())
	if now.Sub(last) < diagGap {
		return false
	}
	lastDiagAt.Store(now.UnixNano())
	if len(text) > 1000 {
		text = text[:1000]
	}
	return publish(tDiag, map[string]any{"line": text, "ts": now.Unix()}, true)
}

func publishLog() bool {
	lines := strings.Split(readFile(agentLog), "\n")
	if len(lines) > logTailN {
		lines = lines[len(lines)-logTailN:]
	}
	text := strings.Join(lines, "\n")
	logf("[log] 回传 %d 行", len(lines))
	return publish(tLog, map[string]any{"lines": text, "ts": time.Now().Unix()}, false)
}

func publishAck(seq int64, ok bool, errMsg string) bool {
	return publish(tAck, AckPayload{Seq: seq, OK: ok, Err: errMsg}, false)
}

// ---------------------------------------------------------------- 持久化

func loadInterval() int64 {
	b, err := os.ReadFile(cfgFile)
	if err != nil {
		return int64(envInt("SY_INTERVAL", 60))
	}
	var cfg struct {
		Interval int64 `json:"interval"`
	}
	if json.Unmarshal(b, &cfg) == nil && cfg.Interval > 0 {
		return clampInterval(cfg.Interval)
	}
	return int64(envInt("SY_INTERVAL", 60))
}

func saveInterval(v int64) {
	_ = os.MkdirAll(stateDir, 0o755)
	b, _ := json.Marshal(map[string]int64{"interval": v})
	_ = os.WriteFile(cfgFile, b, 0o644)
}

func loadSeq() int64 {
	b, err := os.ReadFile(seqFile)
	if err != nil {
		return 0
	}
	var s struct {
		Seq int64 `json:"seq"`
	}
	if json.Unmarshal(b, &s) == nil {
		return s.Seq
	}
	return 0
}

func saveSeq(v int64) {
	_ = os.MkdirAll(stateDir, 0o755)
	b, _ := json.Marshal(map[string]int64{"seq": v})
	_ = os.WriteFile(seqFile, b, 0o644)
}

// ---------------------------------------------------------------- 命令处理

func handleCmd(msg mqtt.Message) {
	raw := strings.TrimSpace(string(msg.Payload()))
	if raw == "" {
		return
	}
	logf("[cmd] %s (retain=%v)", raw, msg.Retained())

	var cmd Command
	if err := json.Unmarshal([]byte(raw), &cmd); err != nil {
		publishDiag("收到非 JSON 命令: " + raw)
		return
	}

	if cmd.Seq != nil {
		if *cmd.Seq <= lastSeq.Load() {
			logf("[cmd] 忽略过期命令 seq=%d (last=%d)", *cmd.Seq, lastSeq.Load())
			return
		}
	}

	ok, errMsg := true, ""

	switch cmd.Cmd {
	case "state":
		publishMeta()
		publishState()

	case "reboot":
		if cmd.Seq != nil {
			publishAck(*cmd.Seq, true, "")
			lastSeq.Store(*cmd.Seq)
			saveSeq(*cmd.Seq)
		}
		logf("[cmd] 执行重启")
		time.Sleep(time.Second)
		_ = exec.Command("reboot").Start()
		return

	case "get":
		switch strings.ToLower(cmd.Key) {
		case "log":
			publishLog()
		case "cfg":
			publish(tCfg, map[string]int64{"interval": interval.Load()}, true)
		default:
			ok, errMsg = false, "未知的 get key: "+cmd.Key
		}

	case "set":
		if strings.ToLower(cmd.Key) == "interval" {
			var v int64
			if json.Unmarshal(cmd.Value, &v) != nil {
				ok, errMsg = false, "interval 必须是整数"
			} else {
				v = clampInterval(v)
				interval.Store(v)
				saveInterval(v)
				publish(tCfg, map[string]int64{"interval": v}, true)
				logf("[cfg] interval => %d", v)
			}
		} else {
			ok, errMsg = false, "未知的 set key: "+cmd.Key
		}

	case "ota":
		ok, errMsg = false, "host 类型设备不支持 OTA"

	default:
		ok, errMsg = false, "不支持的命令: "+cmd.Cmd
	}

	if cmd.Seq != nil {
		lastSeq.Store(*cmd.Seq)
		saveSeq(*cmd.Seq)
		publishAck(*cmd.Seq, ok, errMsg)
	}
	if msg.Retained() {
		publish(tCmd, "", true) // 执行完清掉 retained 命令（协议 §5）
	}
}

func handleCfg(msg mqtt.Message) {
	raw := strings.TrimSpace(string(msg.Payload()))
	if raw == "" {
		return
	}
	var cfg struct {
		Interval int64 `json:"interval"`
	}
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		publishDiag("cfg 不是合法 JSON")
		return
	}
	if cfg.Interval > 0 {
		v := clampInterval(cfg.Interval)
		interval.Store(v)
		saveInterval(v)
		logf("[cfg] 下发 interval=%d", v)
	}
	publishEvent("cfg_applied")
}

// ---------------------------------------------------------------- 主流程

func main() {
	_ = os.MkdirAll(stateDir, 0o755)
	interval.Store(loadInterval())
	lastSeq.Store(loadSeq())

	go cpuSampler()

	opts := mqtt.NewClientOptions()
	opts.AddBroker(fmt.Sprintf("tcp://%s:%d", host, port))
	opts.SetClientID("switchyard-" + deviceID)
	opts.SetUsername(user)
	opts.SetPassword(pass)
	opts.SetKeepAlive(60 * time.Second)
	opts.SetPingTimeout(10 * time.Second)
	opts.SetAutoReconnect(true)
	opts.SetConnectRetry(true)
	opts.SetConnectRetryInterval(5 * time.Second)
	opts.SetCleanSession(true)
	opts.SetWill(tAvail, "offline", 1, true)

	opts.SetOnConnectHandler(func(c mqtt.Client) {
		logf("[mqtt] 已连接 %s:%d", host, port)
		publishMeta()
		publish(tAvail, "online", true)
		publishState()
		c.SubscribeMultiple(
			map[string]byte{tCmd: 1, tCfg: 1},
			func(_ mqtt.Client, m mqtt.Message) {
				defer func() {
					if r := recover(); r != nil {
						logf("[error] 处理消息 panic: %v", r)
						publishDiag(fmt.Sprintf("处理消息出错: %v", r))
					}
				}()
				if m.Topic() == tCmd {
					handleCmd(m)
				} else {
					handleCfg(m)
				}
			})
		publishEvent("boot", "fw", kernelVersion())
	})

	opts.SetConnectionLostHandler(func(_ mqtt.Client, err error) {
		logf("[mqtt] 连接断开: %v（自动重连）", err)
	})

	client = mqtt.NewClient(opts)
	logf("[agent] %s (type=%s) → %s:%d，每 %ds 上报",
		deviceID, devType, host, port, interval.Load())

	if tok := client.Connect(); tok.WaitTimeout(30*time.Second) && tok.Error() != nil {
		logf("[mqtt] 首次连接失败: %v（后台继续重试）", tok.Error())
	}

	// 周期性上报
	go func() {
		last := time.Now()
		for {
			time.Sleep(time.Second)
			if time.Since(last) >= time.Duration(interval.Load())*time.Second {
				last = time.Now()
				publishState()
			}
		}
	}()

	// U 盘插拔事件
	go func() {
		prev := usbPresent()
		for {
			time.Sleep(2 * time.Second)
			cur := usbPresent()
			if cur != prev {
				prev = cur
				if cur {
					publishEvent("usb_attached")
				} else {
					publishEvent("usb_detached")
				}
			}
		}
	}()

	// 常驻
	select {}
}
