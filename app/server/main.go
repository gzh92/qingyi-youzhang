// 情谊有账 后端服务
// 静态文件 + /api/state 数据持久化 + Excel 导入/导出 + 数据落盘备份与版本
//
// 设计要点：
//   - 鉴权：完全依赖飞牛统一网关（网关校验 NAS 登录态并转发 X-Trim-* Header）；
//     本服务不再实现自研口令，避免与网关职责重叠及越权设置风险。
//   - 数据：写入前轮转备份（data.json.bak.N），带 schema 校验，拒绝脏数据覆写。
//   - 导出：除浏览器下载外，支持导出到 NAS 共享目录（TRIM_DATA_SHARE_PATHS）。
//   - 无外部依赖、不联网。
package main

import (
	"archive/zip"
	"bytes"
	"crypto/rand"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"log"
	"math"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	maxStateBytes = 32 << 20 // /api/state 写入上限
	maxXlsxBytes  = 24 << 20 // Excel 上传上限
	maxImportRows = 50000    // 单次导入最大行数
	maxBackups    = 3        // 保留的历史备份份数

	// 字段净化上限（数据来自用户导入的备份，视为不可信输入）
	maxIDLen    = 64     // ID 长度上限
	maxNameLen  = 40     // 对象名/关系 字符数上限
	maxEventLen = 20     // 事由 字符数上限
	maxNoteLen  = 200    // 备注 字符数上限
	maxAmount   = 1e9    // 单笔金额上限
	maxAccounts = 200    // 账本数量上限
	maxRecords  = 200000 // 记录数量上限
)

// 数据文件格式版本，便于后续结构迁移
const dataVersion = 1

func main() {
	port := env("PORT", "6189")
	webroot := env("WWW", "www")
	dataFile := env("DATA_FILE", "data.json")
	sharePaths := env("TRIM_DATA_SHARE_PATHS", "")

	// 按用户隔离数据：网关转发的 X-Trim-Userid 决定数据文件，
	// 满足官方「用户只能访问自己的数据」要求。无 Header 时（直连/端口模式）回落共用文件；
	// 有 Header 但内容非法（疑似伪造/注入）则直接拒绝，绝不回落，防止数据混入共用文件。
	baseDataFile := dataFile
	dataFileFor := func(r *http.Request) (string, bool) {
		uid := strings.TrimSpace(r.Header.Get("X-Trim-Userid"))
		if uid == "" {
			return baseDataFile, true
		}
		// 白名单收敛，防止 Header 注入路径穿越
		safe := make([]rune, 0, len(uid))
		for _, c := range uid {
			if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_' || c == '-' {
				safe = append(safe, c)
			}
		}
		if len(safe) == 0 || len(safe) > 32 {
			return "", false
		}
		ext := filepath.Ext(baseDataFile)
		stem := strings.TrimSuffix(baseDataFile, ext)
		return stem + "-u" + string(safe) + ext, true
	}
	// 统一入口：非法 uid 直接 400
	requireDataFile := func(w http.ResponseWriter, r *http.Request) (string, bool) {
		df, ok := dataFileFor(r)
		if !ok {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid user identity"})
		}
		return df, ok
	}

	var mu sync.Mutex

	mux := http.NewServeMux()

	// ---------- 数据持久化 ----------

	mux.HandleFunc("/api/state", func(w http.ResponseWriter, r *http.Request) {
		df, ok := requireDataFile(w, r)
		if !ok {
			return
		}
		switch r.Method {
		case http.MethodGet:
			mu.Lock()
			b, err := os.ReadFile(df)
			mu.Unlock()
			if err != nil {
				writeJSON(w, http.StatusNotFound, map[string]any{"error": "no data"})
				return
			}
			// 回读时也做一次结构校验，损坏数据不返回给前端（避免前端静默忽略）
			if _, err := validateState(b); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]any{
					"error": "data corrupted", "detail": err.Error(),
				})
				return
			}
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store") // 个人账务数据不进缓存
			w.Write(b)

		case http.MethodPut, http.MethodPost:
			// 超限时 MaxBytesReader 返回 413 并断开，避免静默截断
			r.Body = http.MaxBytesReader(w, r.Body, maxStateBytes)
			b, err := io.ReadAll(r.Body)
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{"error": "read error"})
				return
			}
			clean, err := validateState(b) // 结构校验 + 清理孤儿记录
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{
					"error": "invalid state schema", "detail": err.Error(),
				})
				return
			}
			mu.Lock()
			defer mu.Unlock()
			if err := writeStateWithBackup(df, clean); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})

		default:
			w.Header().Set("Allow", "GET, PUT, POST")
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})

	// 备份列表
	mux.HandleFunc("/api/backups", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		df, ok := requireDataFile(w, r)
		if !ok {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"backups": listBackups(df)})
	})

	// ---------- 数据落盘位置 ----------

	mux.HandleFunc("/api/storage", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		df, ok := requireDataFile(w, r)
		if !ok {
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"sharePaths": splitPaths(sharePaths),
			"dataFile":   df,
		})
	})

	// 导出到 NAS 共享目录（服务端直接落盘，不经浏览器下载）
	mux.HandleFunc("/api/export/to-nas", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		df, ok := requireDataFile(w, r)
		if !ok {
			return
		}
		b, err := os.ReadFile(df)
		if err != nil {
			writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "暂无可导出的数据"})
			return
		}
		clean, err := validateState(b)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "账本数据已损坏，请先恢复备份"})
			return
		}
		targetDir := firstSharePath(sharePaths)
		if targetDir == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "未检测到共享目录，无法导出到 NAS"})
			return
		}
		name := "情谊有账-" + time.Now().Format("20060102-150405") + ".xlsx"
		dest := filepath.Join(targetDir, name)
		// 同一秒内二次导出：追加序号，避免静默覆盖
		if _, err := os.Stat(dest); err == nil {
			found := false
			for i := 2; i <= 99; i++ {
				cand := filepath.Join(targetDir,
					fmt.Sprintf("情谊有账-%s-%02d.xlsx", time.Now().Format("20060102-150405"), i))
				if _, err := os.Stat(cand); err != nil {
					dest, name = cand, filepath.Base(cand)
					found = true
					break
				}
			}
			if !found { // 极端情况：同秒已导出 99 份，随机后缀兜底，绝不覆盖已有文件
				name = "情谊有账-" + time.Now().Format("20060102-150405") + "-" + randomID()[:6] + ".xlsx"
				dest = filepath.Join(targetDir, name)
			}
		}
		xlsx, err := buildXlsxFromState(clean)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "生成表格失败"})
			return
		}
		if err := os.WriteFile(dest, xlsx, 0o644); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "写入失败：" + err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "path": dest, "name": name})
	})

	// ---------- Excel ----------

	mux.HandleFunc("/api/export/xlsx", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		df, ok := requireDataFile(w, r)
		if !ok {
			return
		}
		b, err := os.ReadFile(df)
		if err != nil {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "no data"})
			return
		}
		clean, err := validateState(b)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "data corrupted"})
			return
		}
		xlsx, err := buildXlsxFromState(clean)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "build error"})
			return
		}
		w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
		w.Write(xlsx)
	})

	mux.HandleFunc("/api/import/xlsx", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		// ParseMultipartForm 的参数只是内存阈值，超出部分会落盘 /tmp；
		// 必须先用 MaxBytesReader 限制整体 body，防止恶意大文件撑满磁盘
		r.Body = http.MaxBytesReader(w, r.Body, maxXlsxBytes+(1<<20))
		if err := r.ParseMultipartForm(maxXlsxBytes); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "文件过大或格式不正确"})
			return
		}
		// 超过内存阈值的部分会写入 /tmp 临时文件，必须清理，否则每次导入都泄漏
		defer func() {
			if r.MultipartForm != nil {
				r.MultipartForm.RemoveAll()
			}
		}()
		file, _, err := r.FormFile("file")
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "未收到文件"})
			return
		}
		defer file.Close()
		raw, err := io.ReadAll(io.LimitReader(file, maxXlsxBytes))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "读取失败"})
			return
		}
		rows, skipped, err := parseXlsx(raw)
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "无法解析该 Excel 文件，请使用导出的 .xlsx"})
			return
		}
		truncated := false
		if len(rows) > maxImportRows {
			skipped += len(rows) - maxImportRows
			rows = rows[:maxImportRows]
			truncated = true
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": true, "rows": rows, "skipped": skipped, "truncated": truncated,
		})
	})

	// ---------- 静态资源 ----------
	fs := http.FileServer(http.Dir(webroot))
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 关闭目录列举：非根路径的目录请求一律 404（站点只有扁平的静态文件）
		if p := r.URL.Path; p != "/" && strings.HasSuffix(p, "/") {
			http.NotFound(w, r)
			return
		}
		if p := r.URL.Path; p == "/" || p == "" {
			http.ServeFile(w, r, filepath.Join(webroot, "index.html"))
			return
		}
		fs.ServeHTTP(w, r)
	}))

	// 统一网关模式：监听 gatewaySocket 声明的 Unix Socket。
	sock := env("SOCKET", "")
	var ln net.Listener
	var listenDesc string
	{
		var err error
		if sock != "" {
			_ = os.Remove(sock)
			ln, err = net.Listen("unix", sock)
			listenDesc = "unix:" + sock
			if err == nil {
				_ = os.Chmod(sock, 0o666)
			}
		} else {
			// 兜底端口模式仅绑定本机回环：该应用正常始终走网关 Unix socket，
			// 端口模式下 X-Trim-Userid 无法防伪造，不能暴露到局域网
			addr := "127.0.0.1:" + port
			ln, err = net.Listen("tcp", addr)
			listenDesc = "tcp:" + addr
		}
		if err != nil {
			log.Fatalf("listen %s: %v", listenDesc, err)
		}
	}
	log.Printf("qingyi-youzhang listening %s  web=%s  data=%s  shares=%s",
		listenDesc, webroot, dataFile, sharePaths)

	// 剥离网关前缀
	var handler http.Handler = mux
	if gw := strings.TrimSuffix(env("GATEWAY_PREFIX", ""), "/"); gw != "" {
		handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p := r.URL.Path
			if p == gw || strings.HasPrefix(p, gw+"/") {
				r2 := r.Clone(r.Context())
				r2.URL.Path = strings.TrimPrefix(p, gw)
				if r2.URL.Path == "" {
					r2.URL.Path = "/"
				}
				mux.ServeHTTP(w, r2)
				return
			}
			mux.ServeHTTP(w, r)
		})
	}
	if err := http.Serve(ln, handler); err != nil {
		log.Fatal(err)
	}
}

/* ================= 工具 ================= */

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	b, _ := json.Marshal(v)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	w.Write(b)
}

func splitPaths(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ":") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func firstSharePath(s string) string {
	for _, p := range splitPaths(s) {
		if fi, err := os.Stat(p); err == nil && fi.IsDir() {
			return p
		}
	}
	return ""
}

/* ================= 数据校验与落盘 ================= */

type account struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type record struct {
	ID        string  `json:"id"`
	AccountID string  `json:"accountId"`
	Direction string  `json:"direction"`
	Name      string  `json:"name"`
	Event     string  `json:"event"`
	Relation  string  `json:"relation"`
	Date      string  `json:"date"`
	Amount    float64 `json:"amount"`
	Note      string  `json:"note"`
}

type appState struct {
	Version  int       `json:"version"`
	Accounts []account `json:"accounts"`
	Active   int       `json:"active"`
	Records  []record  `json:"records"`
}

// validateState 校验结构合法性，清理孤儿记录并净化各字段。
// 返回归一化后的 JSON 字节。
//
// 安全说明：数据来自用户导入的备份文件，属于不可信输入。
// 所有字段都必须做长度与字符集收敛，尤其是 ID —— 它会被前端
// 拼进 HTML 属性（data-edit/data-del），一旦含引号即可逃逸属性
// 并注入事件处理器，形成存储型 XSS。
func validateState(b []byte) ([]byte, error) {
	if !json.Valid(b) {
		return nil, fmt.Errorf("not valid json")
	}
	var s appState
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("schema mismatch")
	}
	if len(s.Accounts) == 0 {
		return nil, fmt.Errorf("accounts 不能为空")
	}
	if len(s.Accounts) > maxAccounts {
		return nil, fmt.Errorf("账本数量超出上限")
	}
	if len(s.Records) > maxRecords {
		return nil, fmt.Errorf("记录数量超出上限")
	}
	// 账本 ID：必须是安全字符集的短串；不合规的直接判为非法数据
	ids := make(map[string]bool, len(s.Accounts))
	for i := range s.Accounts {
		id := strings.TrimSpace(s.Accounts[i].ID)
		if id == "" {
			return nil, fmt.Errorf("账本缺少 id")
		}
		if !isSafeID(id) {
			return nil, fmt.Errorf("账本 id 含非法字符")
		}
		s.Accounts[i].ID = id
		s.Accounts[i].Name = clipText(s.Accounts[i].Name, maxNameLen)
		if s.Accounts[i].Name == "" {
			s.Accounts[i].Name = "未命名账本"
		}
		ids[id] = true
	}
	// 记录：清理孤儿 + 逐字段净化
	kept := make([]record, 0, len(s.Records))
	seen := make(map[string]bool, len(s.Records))
	for _, r := range s.Records {
		if !ids[r.AccountID] {
			continue // 孤儿记录
		}
		// ID 非法或重复则重新分配，避免渲染层拼出畸形 DOM，也保证 data 属性可唯一定位
		rid := strings.TrimSpace(r.ID)
		if !isSafeID(rid) || seen[rid] {
			rid = "r" + randomID()
		}
		seen[rid] = true
		r.ID = rid

		r.Name = clipText(r.Name, maxNameLen)
		if r.Name == "" {
			continue // 无对象名，无意义
		}
		r.Event = clipText(r.Event, maxEventLen)
		if r.Event == "" {
			r.Event = "其他"
		}
		r.Relation = clipText(r.Relation, maxNameLen)
		r.Note = clipText(r.Note, maxNoteLen)
		// 方向收敛为枚举
		if r.Direction != "out" {
			r.Direction = "in"
		}
		// 日期格式校验，非法则置空由前端兜底为今天
		if !isISODate(r.Date) {
			r.Date = ""
		}
		// 金额：非有限值/负数归零，超限截断，保留两位
		if !isFiniteNonNeg(r.Amount) {
			r.Amount = 0
		}
		if r.Amount > maxAmount {
			r.Amount = maxAmount
		}
		r.Amount = round2(r.Amount)

		kept = append(kept, r)
	}
	s.Records = kept
	// active 越界修正
	if s.Active < 0 || s.Active >= len(s.Accounts) {
		s.Active = 0
	}
	if s.Version <= 0 {
		s.Version = dataVersion
	}
	return json.Marshal(s)
}

/* ---------- 字段净化工具 ---------- */

// isSafeID 限定 ID 为字母/数字/连字符/下划线，长度 1..64。
// 该字符集不含引号、尖括号、空格，可安全用于 HTML 属性与选择器。
func isSafeID(s string) bool {
	if len(s) == 0 || len(s) > maxIDLen {
		return false
	}
	for _, c := range s {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_':
		default:
			return false
		}
	}
	return true
}

// clipText 按「字符数」截断（非字节），避免中文被截成半个字。
func clipText(s string, max int) string {
	s = strings.TrimSpace(s)
	if max <= 0 || len(s) <= max {
		return s
	}
	// 逐字符累计，遇到超限即停
	n := 0
	for i := range s {
		if n == max {
			return strings.TrimSpace(s[:i])
		}
		n++
	}
	return s
}

func isISODate(s string) bool {
	if len(s) != 10 {
		return false
	}
	_, err := time.Parse("2006-01-02", s)
	return err == nil
}

func isFiniteNonNeg(f float64) bool {
	return !math.IsNaN(f) && !math.IsInf(f, 0) && f >= 0
}

func round2(f float64) float64 {
	return math.Round(f*100) / 100
}

// randomID 生成短随机 ID（仅用小写字母与数字，保证满足 isSafeID）
func randomID() string {
	const chars = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 9)
	for i := range b {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(chars))))
		if err != nil {
			b[i] = chars[i%len(chars)] // 极端兜底，不阻断正常流程
			continue
		}
		b[i] = chars[n.Int64()]
	}
	return string(b)
}

// writeStateWithBackup 原子写入 + 轮转历史备份。
// 顺序：先写 tmp（写失败时主文件与备份均不受影响），成功后再轮转。
func writeStateWithBackup(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("无法创建数据目录")
	}
	// 原子写：先写临时文件（若磁盘满等失败，此时主文件仍完好）
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("写入失败：%v", err)
	}
	// 轮转：data.json -> data.json.bak.1 -> .2 ...
	for i := maxBackups - 1; i >= 1; i-- {
		old := fmt.Sprintf("%s.bak.%d", path, i)
		newer := fmt.Sprintf("%s.bak.%d", path, i+1)
		if _, err := os.Stat(old); err == nil {
			if err := os.Rename(old, newer); err != nil {
				log.Printf("backup rotate %s: %v", newer, err)
			}
		}
	}
	if _, err := os.Stat(path); err == nil {
		if err := os.Rename(path, path+".bak.1"); err != nil {
			log.Printf("backup rotate %s: %v", path+".bak.1", err)
		}
	}
	if err := os.Rename(tmp, path); err != nil {
		// rename 失败极罕见（跨设备等）；尝试恢复主文件并清理残留 tmp
		if e := os.Rename(path+".bak.1", path); e != nil {
			log.Printf("CRITICAL: 主文件落盘失败且回滚失败 path=%s: %v（主文件可能缺失，请检查备份）", path, e)
		}
		if e := os.Remove(tmp); e != nil {
			log.Printf("cleanup tmp %s: %v", tmp, e)
		}
		return fmt.Errorf("落盘失败：%v", err)
	}
	return nil
}

type backupInfo struct {
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	ModTime string `json:"modTime"`
}

func listBackups(path string) []backupInfo {
	out := []backupInfo{}
	for i := 1; i <= maxBackups; i++ {
		p := fmt.Sprintf("%s.bak.%d", path, i)
		fi, err := os.Stat(p)
		if err != nil {
			continue
		}
		out = append(out, backupInfo{
			Name:    filepath.Base(p),
			Size:    fi.Size(),
			ModTime: fi.ModTime().Format("2006-01-02 15:04:05"),
		})
	}
	return out
}

/* ================= Excel 导出 ================= */

var xlsxHeaders = []string{"账本", "日期", "方向", "对象", "事件", "关系", "金额", "备注"}

// 列宽（字符数），让导出表格开箱即用
var xlsxColWidths = []float64{16, 12, 6, 14, 12, 12, 12, 24}

type sstBuilder struct {
	index map[string]int
	list  []string
}

func newSST() *sstBuilder { return &sstBuilder{index: map[string]int{}} }

func (s *sstBuilder) add(v string) int {
	if i, ok := s.index[v]; ok {
		return i
	}
	i := len(s.list)
	s.index[v] = i
	s.list = append(s.list, v)
	return i
}

func buildXlsxFromState(clean []byte) ([]byte, error) {
	var s appState
	if err := json.Unmarshal(clean, &s); err != nil {
		return nil, err
	}
	accName := make(map[string]string, len(s.Accounts))
	for _, a := range s.Accounts {
		accName[a.ID] = a.Name
	}
	return buildXlsx(accName, s.Records)
}

type xcell struct {
	strIdx int
	num    string
	style  int // 0=通用 1=强调 2=金额
}

func buildXlsx(accName map[string]string, records []record) ([]byte, error) {
	sst := newSST()
	dirName := func(d string) string {
		if d == "out" {
			return "送"
		}
		return "收"
	}
	buildRow := func(style int, vals ...string) []xcell {
		row := make([]xcell, len(vals))
		for i, v := range vals {
			row[i] = xcell{strIdx: sst.add(v), style: style}
		}
		return row
	}

	rows := [][]xcell{buildRow(1, xlsxHeaders...)}

	var totalIn, totalOut float64
	for _, rc := range records {
		if rc.Direction == "out" {
			totalOut += rc.Amount
		} else {
			totalIn += rc.Amount
		}
		am := strconv.FormatFloat(rc.Amount, 'f', -1, 64)
		r := buildRow(0,
			accName[rc.AccountID], rc.Date, dirName(rc.Direction), rc.Name,
			rc.Event, rc.Relation, am, rc.Note)
		r[6] = xcell{num: am, style: 2} // 金额列：数字 + 千分位
		rows = append(rows, r)
	}

	// 汇总行：8 列（合计 + 5 空 + 金额列结余 + 备注列收送汇总）
	if len(records) > 0 {
		sum := buildRow(1, "合计", "", "", "", "", "", trimNum(totalIn-totalOut),
			fmt.Sprintf("收 %s / 送 %s", trimNum(totalIn), trimNum(totalOut)))
		sum[6].style = 2
		rows = append(rows, sum)
	}

	var sb strings.Builder
	sb.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>`)
	sb.WriteString(`<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">`)
	sb.WriteString(`<sheetViews><sheetView workbookViewId="0"><pane ySplit="1" topLeftCell="A2" activePane="bottomLeft" state="frozen"/></sheetView></sheetViews>`)
	sb.WriteString(`<cols>`)
	for i, w := range xlsxColWidths {
		sb.WriteString(fmt.Sprintf(`<col min="%d" max="%d" width="%g" customWidth="1"/>`, i+1, i+1, w))
	}
	sb.WriteString(`</cols>`)
	sb.WriteString(`<sheetData>`)
	for ri, row := range rows {
		rn := ri + 1
		sb.WriteString(fmt.Sprintf(`<row r="%d">`, rn))
		for ci, cell := range row {
			ref := colName(ci) + strconv.Itoa(rn)
			st := ""
			if cell.style > 0 {
				st = fmt.Sprintf(` s="%d"`, cell.style)
			}
			if cell.num != "" {
				sb.WriteString(`<c r="` + ref + `"` + st + `><v>` + cell.num + `</v></c>`)
			} else {
				sb.WriteString(fmt.Sprintf(`<c r="%s"%s t="s"><v>%d</v></c>`, ref, st, cell.strIdx))
			}
		}
		sb.WriteString(`</row>`)
	}
	sb.WriteString(`</sheetData></worksheet>`)

	var ss strings.Builder
	ss.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>`)
	ss.WriteString(`<sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" count="` +
		strconv.Itoa(len(sst.list)) + `" uniqueCount="` + strconv.Itoa(len(sst.list)) + `">`)
	for _, v := range sst.list {
		ss.WriteString(`<si><t xml:space="preserve">` + xmlEsc(v) + `</t></si>`)
	}
	ss.WriteString(`</sst>`)

	// styles.xml：通用 / 表头强调 / 金额千分位
	styles := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
		`<styleSheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">` +
		`<fonts count="2">` +
		`<font><sz val="11"/><name val="微软雅黑"/></font>` +
		`<font><b/><sz val="11"/><name val="微软雅黑"/></font>` +
		`</fonts>` +
		`<fills count="3">` +
		`<fill><patternFill patternType="none"/></fill>` +
		`<fill><patternFill patternType="gray125"/></fill>` +
		`<fill><patternFill patternType="solid"><fgColor rgb="FFF4EDE1"/><bgColor indexed="64"/></patternFill></fill>` +
		`</fills>` +
		`<borders count="1"><border><left/><right/><top/><bottom/><diagonal/></border></borders>` +
		`<cellStyleXfs count="1"><xf numFmtId="0" fontId="0" fillId="0" borderId="0"/></cellStyleXfs>` +
		`<cellXfs count="3">` +
		`<xf numFmtId="0" fontId="0" fillId="0" borderId="0" xfId="0"/>` +
		`<xf numFmtId="0" fontId="1" fillId="2" borderId="0" xfId="0" applyFont="1" applyFill="1"/>` +
		`<xf numFmtId="4" fontId="0" fillId="0" borderId="0" xfId="0" applyNumberFormat="1"/>` +
		`</cellXfs>` +
		`</styleSheet>`

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	parts := map[string]string{
		"[Content_Types].xml": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
			`<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">` +
			`<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>` +
			`<Default Extension="xml" ContentType="application/xml"/>` +
			`<Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/>` +
			`<Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>` +
			`<Override PartName="/xl/sharedStrings.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sharedStrings+xml"/>` +
			`<Override PartName="/xl/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.styles+xml"/>` +
			`</Types>`,
		"_rels/.rels": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
			`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
			`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/>` +
			`</Relationships>`,
		"xl/workbook.xml": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
			`<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">` +
			`<sheets><sheet name="情谊有账" sheetId="1" r:id="rId1"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
			`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
			`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/>` +
			`<Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/sharedStrings" Target="sharedStrings.xml"/>` +
			`<Relationship Id="rId3" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/>` +
			`</Relationships>`,
		"xl/styles.xml":            styles,
		"xl/worksheets/sheet1.xml": sb.String(),
		"xl/sharedStrings.xml":     ss.String(),
	}
	names := make([]string, 0, len(parts))
	for n := range parts {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		w, err := zw.Create(name)
		if err != nil {
			return nil, err
		}
		if _, err := io.WriteString(w, parts[name]); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func trimNum(f float64) string {
	return strconv.FormatFloat(f, 'f', -1, 64)
}

// colName 返回 0 起始列号对应的列名（0->A, 25->Z, 26->AA）
func colName(i int) string {
	name := ""
	for i >= 0 {
		name = string(rune('A'+i%26)) + name
		i = i/26 - 1
	}
	return name
}

func xmlEsc(s string) string {
	var b strings.Builder
	xml.EscapeText(&b, []byte(s))
	return b.String()
}

/* ================= Excel 导入 ================= */

type xlsxSharedStrings struct {
	Si []struct {
		T []string `xml:"t"`
		R []struct {
			T string `xml:"t"`
		} `xml:"r"`
	} `xml:"si"`
}

type xlsxCell struct {
	R  string `xml:"r,attr"`
	T  string `xml:"t,attr"`
	V  string `xml:"v"`
	Is struct {
		T string `xml:"t"`
	} `xml:"is"`
}

type xlsxSheet struct {
	SheetData struct {
		Rows []struct {
			Cells []xlsxCell `xml:"c"`
		} `xml:"row"`
	} `xml:"sheetData"`
}

// parseXlsx 解析 xlsx，返回行数据与被跳过行数。
func parseXlsx(raw []byte) ([]map[string]any, int, error) {
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return nil, 0, err
	}
	var shared []string
	var sheetXML []byte
	for _, f := range zr.File {
		switch {
		case f.Name == "xl/sharedStrings.xml":
			rc, err := f.Open()
			if err != nil {
				return nil, 0, err
			}
			content, err := io.ReadAll(io.LimitReader(rc, 8<<20))
			rc.Close()
			if err != nil {
				return nil, 0, fmt.Errorf("读取 sharedStrings 失败：%v", err)
			}
			var s xlsxSharedStrings
			if err := xml.Unmarshal(content, &s); err == nil {
				for _, si := range s.Si {
					var t string
					for _, r := range si.R {
						t += r.T
					}
					if t == "" {
						for _, tt := range si.T {
							t += tt
						}
					}
					shared = append(shared, t)
				}
			}
		// 只取第一个 worksheet，避免多 sheet 时被后者覆盖
		case sheetXML == nil &&
			strings.HasPrefix(f.Name, "xl/worksheets/") &&
			strings.HasSuffix(f.Name, ".xml"):
			rc, err := f.Open()
			if err != nil {
				return nil, 0, err
			}
			sheetXML, err = io.ReadAll(io.LimitReader(rc, 16<<20))
			rc.Close()
			if err != nil {
				return nil, 0, fmt.Errorf("读取工作表失败：%v", err)
			}
		}
	}
	if sheetXML == nil {
		return nil, 0, fmt.Errorf("no sheet")
	}
	var sheet xlsxSheet
	if err := xml.Unmarshal(sheetXML, &sheet); err != nil {
		return nil, 0, err
	}

	cellVal := func(c xlsxCell) string {
		switch c.T {
		case "s":
			if i, err := strconv.Atoi(c.V); err == nil && i >= 0 && i < len(shared) {
				return shared[i]
			}
			return ""
		case "inlineStr":
			return c.Is.T
		default:
			return c.V
		}
	}

	rows := []map[string]any{}
	skipped := 0
	for ri, row := range sheet.SheetData.Rows {
		if ri == 0 { // 跳过表头
			continue
		}
		vals := map[int]string{}
		for _, c := range row.Cells {
			if idx := colLetterIdx(c.R); idx >= 0 {
				vals[idx] = cellVal(c)
			}
		}
		acc, date, dir, name := vals[0], vals[1], vals[2], vals[3]
		evt, rel, amt, note := vals[4], vals[5], vals[6], vals[7]
		if strings.TrimSpace(acc) == "合计" { // 跳过汇总行
			continue
		}
		if strings.TrimSpace(name) == "" && strings.TrimSpace(acc) == "" {
			continue
		}
		// 数据校验：无对象名的行跳过
		if strings.TrimSpace(name) == "" {
			skipped++
			continue
		}
		// 金额校验：非数字或负数跳过
		if strings.TrimSpace(amt) != "" {
			if f, err := strconv.ParseFloat(strings.TrimSpace(amt), 64); err != nil || f < 0 {
				skipped++
				continue
			}
		}
		rows = append(rows, map[string]any{
			"account": acc, "date": date, "direction": dir, "name": name,
			"event": evt, "relation": rel, "amount": amt, "note": note,
		})
	}
	return rows, skipped, nil
}

// colLetterIdx 将列引用（如 "A1"、"AA3"）转为 0 起始列号，支持多字母列。
func colLetterIdx(ref string) int {
	col := 0
	got := false
	for i := 0; i < len(ref); i++ {
		c := ref[i]
		switch {
		case c >= 'A' && c <= 'Z':
			col = col*26 + int(c-'A'+1)
			got = true
		case c >= 'a' && c <= 'z':
			col = col*26 + int(c-'a'+1)
			got = true
		default:
			if got {
				return col - 1
			}
			return -1
		}
	}
	if !got {
		return -1
	}
	return col - 1
}
