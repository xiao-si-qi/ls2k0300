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
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"math"
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

	// caps：只放「必须由 Switchyard 操作、HA 做不到」的能力。
	// 本板没有 OTA / 布局下发，所以是空表——重启这类 HA 能表达的动作
	// 走设备自己的 HA Discovery，不占 caps。
	caps = []string{}

	// metrics：只读遥测。用于两件事：
	//   1. 设备自己发布 HA Discovery（HA 直接拿到实体，不经过 Switchyard）
	//   2. 写进 meta 供 Switchyard 界面展示（台账）
	metrics = []Metric{
		{Key: "fw", Name: "固件", Icon: "mdi:chip"},
		{Key: "rssi", Name: "信号", Unit: "dBm", Icon: "mdi:wifi", DeviceClass: "signal_strength",
			Precision: iptr(0)},
		{Key: "uptime", Name: "运行时长", Unit: "s", Icon: "mdi:clock-outline",
			DeviceClass: "duration", StateClass: "total_increasing", Precision: iptr(0)},
		{Key: "ip_eth", Name: "有线 IP", Icon: "mdi:ethernet"},
		{Key: "ip_wlan", Name: "无线 IP", Icon: "mdi:wifi"},
		{Key: "cpu_load", Name: "CPU 占用", Unit: "%", Icon: "mdi:cpu-64-bit", Precision: iptr(1)},
		{Key: "mem_used_pct", Name: "内存占用", Unit: "%", Icon: "mdi:memory", Precision: iptr(1)},
		{Key: "disk_used_pct", Name: "存储占用", Unit: "%", Icon: "mdi:harddisk", Precision: iptr(1)},
		{Key: "disk_free_mb", Name: "存储剩余", Unit: "MB", Icon: "mdi:database", Precision: iptr(0)},
		{Key: "heap", Name: "可用内存", Unit: "KB", Icon: "mdi:memory", Precision: iptr(0)},
	}

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
	IP         *string  `json:"ip"`       // 主 IP（有线优先），Switchyard 台账用
	IPEth      *string  `json:"ip_eth"`   // eth0 的 IPv4
	IPWlan     *string  `json:"ip_wlan"`  // wlan0 的 IPv4
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
	DiskUsedPct *float64 `json:"disk_used_pct"`
	USB        bool     `json:"usb"`
	Interval   int64    `json:"interval"`
}

type Metric struct {
	Key         string `json:"key"`
	Name        string `json:"name"`
	Kind        string `json:"kind,omitempty"` // 空 = sensor；binary_sensor 表示布尔
	Unit        string `json:"unit,omitempty"`
	Icon        string `json:"icon,omitempty"`
	DeviceClass string `json:"device_class,omitempty"`
	StateClass  string `json:"state_class,omitempty"`
	Object      string `json:"object,omitempty"`
	Precision   *int   `json:"precision,omitempty"` // HA 的 suggested_display_precision
}

func iptr(n int) *int { return &n }

// HAEntity 记录本设备往 HA 发过哪些实体，供 Switchyard 的「删除设备」照着清。
type HAEntity struct {
	Component string `json:"component"`
	Object    string `json:"object"`
}

type MetaPayload struct {
	Type       string     `json:"type"`
	Caps       []string   `json:"caps"`
	Name       string     `json:"name"`
	Model      string     `json:"model"`
	Interval   int64      `json:"interval"`
	Metrics    []Metric   `json:"metrics"`
	HAEntities []HAEntity `json:"ha_entities"`
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

// cpuSampler 每秒采一次 /proc/stat，算 CPU 占用率。
// 结果保留 1 位小数：/proc/stat 的计数器是整数，直接除会得到 10.1010101… 这种玩意。
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
		if pct > 100 {
			pct = 100 // 计数器偶有跨核累加误差，钳一下
		}
		pct = math.Round(pct*10) / 10
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

// ifaceIP 取指定前缀（eth / wlan）的第一块网卡的 IPv4。
func ifaceIP(prefix string) *string {
	out, err := exec.Command("ip", "-4", "-o", "addr", "show", "scope", "global").Output()
	if err != nil {
		return nil
	}
	for _, line := range strings.Split(string(out), "\n") {
		m := ipRe.FindStringSubmatch(line)
		if m != nil && strings.HasPrefix(m[1], prefix) {
			addr := m[2]
			return &addr
		}
	}
	return nil
}

// primaryIP 返回对外展示的主 IP：有线优先，没有有线才用无线。
func primaryIP() *string {
	if ip := ifaceIP("eth"); ip != nil {
		return ip
	}
	return ifaceIP("wlan")
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

// diskUsage 返回根文件系统剩余 MB 和已用百分比。
func diskUsage() (freeMB *int64, usedPct *float64) {
	var st syscall.Statfs_t
	if err := syscall.Statfs("/", &st); err != nil {
		return nil, nil
	}
	frsize := float64(st.Frsize)
	total := float64(st.Blocks) * frsize
	free := float64(st.Bavail) * frsize
	f := int64(free / 1048576)
	freeMB = &f
	if total > 0 {
		p := (total - free) / total * 100
		p = float64(int(p*10)) / 10
		usedPct = &p
	}
	return freeMB, usedPct
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
	freeMB, diskPct := diskUsage()
	return StatePayload{
		ID:          deviceID,
		Type:        devType,
		FW:          kernelVersion(),
		IP:          primaryIP(),
		IPEth:       ifaceIP("eth"),
		IPWlan:      ifaceIP("wlan"),
		RSSI:        wlanRSSI(),
		Uptime:      uptimeSeconds(),
		Heap:        availKB,
		TS:          time.Now().Unix(),
		CPULoad:     load,
		MemTotalKB:  totalKB,
		MemFreeKB:   availKB,
		MemUsedPct:  usedPct,
		DiskFreeMB:  freeMB,
		DiskUsedPct: diskPct,
		USB:         usbPresent(),
		Interval:    interval.Load(),
	}
}

func buildMeta() MetaPayload {
	return MetaPayload{
		Type:       devType,
		Caps:       caps,
		Name:       deviceName,
		Model:      model,
		Interval:   interval.Load(),
		Metrics:    metrics,
		HAEntities: buildHAEntities(),
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

// ---------------------------------------------------------------- HA Discovery
//
// 分界线（v0.3 起）：
//
//	HA 能直接表达的（传感器/开关/按钮/数字）→ 设备自己发布 HA MQTT Discovery。
//	  这样实体不经过 Switchyard，插件挂了/升级坏了，HA 的实体和自动化都不受影响。
//
//	只有 Switchyard 才能做的（OTA 分发、布局下发、批量配置）→ switchyard/<id>/cmd。
//
// 状态主题仍用 switchyard/<id>/state：它是数据源，HA 和 Switchyard 都读它，
// 但都只是"读"，谁挂了都不影响对方。
//
// 本设备的 HA 实体：
//	  10 个 metrics  → sensor
//	  在线状态       → binary_sensor（读 availability）
//	  重启           → button（HA 下发到 switchyard/<id>/ha/reboot/set）

const haPrefix = "homeassistant"

func haCmdTopic(obj string) string {
	return "switchyard/" + deviceID + "/ha/" + obj + "/set"
}

func haConfigTopic(component, obj string) string {
	return fmt.Sprintf("%s/%s/switchyard_%s/%s/config", haPrefix, component, deviceID, obj)
}

func haDeviceBlock() map[string]any {
	return map[string]any{
		"identifiers":  []string{"switchyard_" + deviceID},
		"name":         deviceName,
		"manufacturer": "Switchyard",
		"model":        model,
		"sw_version":   kernelVersion(),
	}
}

// buildHAEntities 列出本设备往 HA 发布过哪些实体（供 Switchyard 删除设备时清理）。
func buildHAEntities() []HAEntity {
	out := make([]HAEntity, 0, len(metrics)+2)
	for _, m := range metrics {
		comp := "sensor"
		if m.Kind == "binary_sensor" {
			comp = "binary_sensor"
		}
		out = append(out, HAEntity{Component: comp, Object: metricObject(m)})
	}
	out = append(out,
		HAEntity{Component: "binary_sensor", Object: "online"},
		HAEntity{Component: "button", Object: "reboot"},
	)
	return out
}

func metricObject(m Metric) string {
	if m.Object != "" {
		return m.Object
	}
	return m.Key
}

// publishHADiscovery 发布全部实体配置（retained）。连接建立时调用一次即可，
// broker 会一直保留；重连补发是为了应对 broker 丢 retained 的情况。
func publishHADiscovery() {
	dev := haDeviceBlock()
	origin := map[string]any{"name": "switchyard-agent"}
	n := 0

	for _, m := range metrics {
		obj := metricObject(m)
		kind := m.Kind
		if kind == "" {
			kind = "sensor"
		}
		cfg := map[string]any{
			"name":            m.Name,
			"unique_id":       "switchyard_" + deviceID + "_" + obj,
			"object_id":       deviceID + "_" + obj,
			"state_topic":     tState,
			"device":          dev,
			"origin":          origin,
			"entity_category": "diagnostic",
		}
		if kind == "binary_sensor" {
			cfg["value_template"] = fmt.Sprintf("{{ 'ON' if value_json.%s else 'OFF' }}", m.Key)
			cfg["payload_on"] = "ON"
			cfg["payload_off"] = "OFF"
		} else {
			cfg["value_template"] = fmt.Sprintf("{{ value_json.%s }}", m.Key)
			if m.Unit != "" {
				cfg["unit_of_measurement"] = m.Unit
				sc := m.StateClass
				if sc == "" {
					sc = "measurement"
				}
				cfg["state_class"] = sc
			}
		}
		if m.Icon != "" {
			cfg["icon"] = m.Icon
		}
		if m.DeviceClass != "" {
			cfg["device_class"] = m.DeviceClass
		}
		if m.Precision != nil {
			// HA 面板按这个精度显示，避免 10.1010101… 这种值直接怼到界面上
			cfg["suggested_display_precision"] = *m.Precision
		}
		if publish(haConfigTopic(kind, obj), cfg, true) {
			n++
		}
	}

	// 在线状态：读 availability（LWT 维护）
	online := map[string]any{
		"name":         "在线",
		"unique_id":    "switchyard_" + deviceID + "_online",
		"object_id":    deviceID + "_online",
		"state_topic":  tAvail,
		"payload_on":   "online",
		"payload_off":  "offline",
		"device_class": "connectivity",
		"device":       dev,
		"origin":       origin,
	}
	if publish(haConfigTopic("binary_sensor", "online"), online, true) {
		n++
	}

	// 重启按钮：HA 点一下 → 发到 ha/reboot/set
	reboot := map[string]any{
		"name":          "重启",
		"unique_id":     "switchyard_" + deviceID + "_reboot",
		"object_id":     deviceID + "_reboot",
		"command_topic": haCmdTopic("reboot"),
		"payload_press": "PRESS",
		"icon":          "mdi:restart",
		"device":        dev,
		"origin":        origin,
	}
	if publish(haConfigTopic("button", "reboot"), reboot, true) {
		n++
	}

	logf("[discovery] 发布 %d 个 HA 实体（不经过 Switchyard）", n)
}

// handleHACmd 处理 HA 侧下发的控制命令（主题 switchyard/<id>/ha/<obj>/set）。
func handleHACmd(obj string, payload []byte) {
	body := strings.TrimSpace(string(payload))
	logf("[ha-cmd] %s = %q", obj, body)
	switch obj {
	case "reboot":
		logf("[ha-cmd] 执行重启")
		time.Sleep(300 * time.Millisecond)
		_ = exec.Command("reboot").Start()
	default:
		logf("[ha-cmd] 未知对象: %s", obj)
	}
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

// mqttEndpoint 按配置决定明文还是 TLS，返回 broker URL 和 TLS 配置。
//
// 明文时 broker 在同一个局域网（路由器已做了 DNS/NAT 处理）就够了；
// 一旦 broker 在公网，凭据和命令全程明文，必须开 TLS。
//
//	MQTT_TLS=1            启用 TLS（默认 0 = 明文）
//	MQTT_CA_FILE=路径     自定义 CA（企业内网/自签证书）；留空用系统根证书
//	MQTT_INSECURE=1       跳过证书与主机名校验，**仅调试用**
//	MQTT_SERVER_NAME=xxx  覆盖 SNI / 证书校验用的主机名，默认用 MQTT_HOST
func mqttEndpoint() (string, *tls.Config, error) {
	if envInt("MQTT_TLS", 0) != 1 {
		return fmt.Sprintf("tcp://%s:%d", host, port), nil, nil
	}

	tlsCfg := &tls.Config{
		MinVersion: tls.VersionTLS12,
		ServerName: env("MQTT_SERVER_NAME", host),
	}

	if caFile := env("MQTT_CA_FILE", ""); caFile != "" {
		pem, err := os.ReadFile(caFile)
		if err != nil {
			return "", nil, fmt.Errorf("读取 CA 文件失败: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return "", nil, fmt.Errorf("CA 文件里没有可用的证书: %s", caFile)
		}
		tlsCfg.RootCAs = pool
	}

	if envInt("MQTT_INSECURE", 0) == 1 {
		tlsCfg.InsecureSkipVerify = true
		logf("[mqtt] ⚠ 已禁用证书校验（MQTT_INSECURE=1），仅限调试")
	}

	// paho 认 ssl:// 作为 TLS 传输
	return fmt.Sprintf("ssl://%s:%d", host, port), tlsCfg, nil
}

// ---------------------------------------------------------------- 主流程

func main() {
	_ = os.MkdirAll(stateDir, 0o755)
	interval.Store(loadInterval())
	lastSeq.Store(loadSeq())

	go cpuSampler()

	brokerURL, tlsCfg, err := mqttEndpoint()
	if err != nil {
		logf("[fatal] %v", err)
		os.Exit(1)
	}

	opts := mqtt.NewClientOptions()
	opts.AddBroker(brokerURL)
	if tlsCfg != nil {
		opts.SetTLSConfig(tlsCfg)
		logf("[mqtt] 使用 TLS 连接 %s（CA=%s，SNI=%s）",
			brokerURL, env("MQTT_CA_FILE", "系统根证书"), tlsCfg.ServerName)
	} else {
		logf("[mqtt] 使用明文连接 %s", brokerURL)
	}
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
		publishHADiscovery() // 实体由设备自己注册，不经过 Switchyard
		c.SubscribeMultiple(
			map[string]byte{
				tCmd: 1,
				tCfg: 1,
				// HA 下发的控制命令（button/number/switch 的 command_topic）
				"switchyard/" + deviceID + "/ha/+/set": 1,
			},
			func(_ mqtt.Client, m mqtt.Message) {
				defer func() {
					if r := recover(); r != nil {
						logf("[error] 处理消息 panic: %v", r)
						publishDiag(fmt.Sprintf("处理消息出错: %v", r))
					}
				}()
				switch {
				case m.Topic() == tCmd:
					handleCmd(m)
				case m.Topic() == tCfg:
					handleCfg(m)
				default:
					// switchyard/<id>/ha/<obj>/set
					parts := strings.Split(m.Topic(), "/")
					if len(parts) == 5 && parts[0] == "switchyard" && parts[2] == "ha" && parts[4] == "set" {
						handleHACmd(parts[3], m.Payload())
					}
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

	// 定期重发 HA discovery（retained）。
	// 保险起见：broker 丢过 retained、或者别的组件误清了配置时，设备能自己补回来，
	// 不需要人去 HA 里点"重新加载"。12 条 retained 消息，开销可忽略。
	go func() {
		for {
			time.Sleep(10 * time.Minute)
			if client != nil && client.IsConnectionOpen() {
				publishHADiscovery()
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
