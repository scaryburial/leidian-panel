// 域名功能开启后，把面板自身的监听端口搬到 Cloudflare 支持的端口上。
//
// 背景：面板默认监听 33441，而 **33441 不在 Cloudflare 代理支持的端口列表里**。
// 于是出现一个很别扭的状态——开了域名之后，订阅能走域名，
// 但想登录面板仍然只能 `http://<IP>:33441/ui3344/`，域名形同只服务了一半。
//
// 因此启用域名功能时顺手把面板搬到 8443：
//   - 它在 CF 的 HTTPS 端口列表（443/2053/2083/2087/2096/8443）里，
//     所以 `https://<子域名>:8443/ui3344/` 这条登录路径能真正打通；
//   - 它同时是面板历来惯用的 TLS 端口。
//
// 三件事必须一起做，否则搬过去也登不上：
//  1. 改 webPort；
//  2. 把 webCertFile/webKeyFile 指到刚签发的域名证书，否则浏览器会因证书不匹配报警；
//  3. 让出 8443——预设里有一个 `VLESS-速度-8443` 入站正占着这个端口
//     （两个进程不能监听同一端口）。这里临时停用它并记下 id，
//     停用域名功能时自动恢复，所以是可逆的。
//
// 所有原值都写进 domainConfig 持久化，Disable 时逐项还原。
package domain

import (
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service/panel"
)

// cfPanelPort 是启用域名后面板要搬到的端口。必须留在 Cloudflare 的 HTTPS
// 端口列表内，否则域名登录依旧不通。
const cfPanelPort = 8443

// panelPortState 记录搬迁前的面板端口/证书，以及为腾出端口而被临时停用的入站。
// 字段直接嵌入 storedState（见 domain.go），随 domainConfig 一起持久化。
type panelPortState struct {
	PrevPort      int    `json:"prevPanelPort,omitempty"`
	PrevCertFile  string `json:"prevPanelCertFile,omitempty"`
	PrevKeyFile   string `json:"prevPanelKeyFile,omitempty"`
	Moved         bool   `json:"panelMoved,omitempty"`
	ParkedInbound int    `json:"parkedInboundId,omitempty"`
	// 为腾出面板端口而被挪走的入站新端口（0 表示当时是停用而非挪端口）。
	ParkedPort int `json:"parkedInboundNewPort,omitempty"`

	// 订阅服务的原证书，停用域名时一并还原。
	PrevSubCertFile string `json:"prevSubCertFile,omitempty"`
	PrevSubKeyFile  string `json:"prevSubKeyFile,omitempty"`
}

// freePortForPanel 腾出 cfPanelPort。
//
// 优先做法是「把占用该端口的入站挪到下一个空闲端口并保持启用」——这样开了域名之后，
// 原协议只是换了端口，仍然可用，用户不会平白少一个入口。只有实在找不到空闲端口时，
// 才退回旧行为（临时停用该入站，停用域名功能时恢复）。
//
// 只改 port，不动 tag/remark：tag 会被路由规则、中转/反向代理的 scope=inbounds 引用，
// 改掉会连带破坏那些配置；remark 保持原样也让 create-inbounds.py 仍然按它幂等跳过。
func freePortForPanel() (parkedID int, movedPort int, warnings []string, err error) {
	db := database.GetDB()
	var ib model.Inbound
	if err := db.Where("port = ?", cfPanelPort).Where("enable = ?", true).
		Limit(1).Find(&ib).Error; err != nil {
		return 0, 0, nil, err
	}
	if ib.Id == 0 {
		return 0, 0, nil, nil // 端口本来就是空的
	}

	newPort, ok := nextFreeInboundPort(cfPanelPort + 1)
	if !ok {
		if err := db.Model(&model.Inbound{}).Where("id = ?", ib.Id).
			Update("enable", false).Error; err != nil {
			return 0, 0, nil, err
		}
		warnings = append(warnings, fmt.Sprintf(
			"端口 %d 原本被入站「%s」占用，且附近没有空闲端口，已临时停用以免与面板抢端口（停用域名功能时会自动恢复）",
			cfPanelPort, ib.Remark))
		return ib.Id, 0, warnings, nil
	}

	if err := db.Model(&model.Inbound{}).Where("id = ?", ib.Id).
		Update("port", newPort).Error; err != nil {
		return 0, 0, nil, err
	}
	warnings = append(warnings, fmt.Sprintf(
		"端口 %d 原本被入站「%s」占用，已把该入站挪到 %d 并保持启用（停用域名功能时会挪回 %d）",
		cfPanelPort, ib.Remark, newPort, cfPanelPort))
	return ib.Id, newPort, warnings, nil
}

// nextFreeInboundPort 从 from 开始找一个既没被其它入站占用、也没有进程在监听的端口。
func nextFreeInboundPort(from int) (int, bool) {
	var used []int
	_ = database.GetDB().Model(&model.Inbound{}).Pluck("port", &used).Error
	taken := make(map[int]bool, len(used))
	for _, p := range used {
		taken[p] = true
	}
	for port := from; port < from+200 && port <= 65535; port++ {
		if taken[port] || !portFreeOnHost(port) {
			continue
		}
		return port, true
	}
	return 0, false
}

// portFreeOnHost 通过真正试听一次来判断端口是否空闲。
func portFreeOnHost(port int) bool {
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return false
	}
	_ = ln.Close()
	return true
}

// releaseParkedInbound 还原为腾端口而做的改动：挪过端口的挪回 cfPanelPort，
// 被停用的重新启用。
func releaseParkedInbound(id int, movedPort int) {
	if id == 0 {
		return
	}
	db := database.GetDB()
	if movedPort > 0 {
		_ = db.Model(&model.Inbound{}).Where("id = ?", id).
			Update("port", cfPanelPort).Error
		return
	}
	_ = db.Model(&model.Inbound{}).Where("id = ?", id).
		Update("enable", true).Error
}

// movePanelToCFPort 把面板搬到 cfPanelPort 并使用域名证书，返回搬迁前的状态。
func (m *Manager) movePanelToCFPort(certDir string) (panelPortState, []string, error) {
	var st panelPortState
	cur, err := m.settingService.GetPort()
	if err != nil {
		return st, nil, fmt.Errorf("读取面板端口失败：%w", err)
	}
	curCert, _ := m.settingService.GetCertFile()
	curKey, _ := m.settingService.GetKeyFile()
	curSubCert, _ := m.settingService.GetSubCertFile()
	curSubKey, _ := m.settingService.GetSubKeyFile()

	st.PrevPort, st.PrevCertFile, st.PrevKeyFile = cur, curCert, curKey
	st.PrevSubCertFile, st.PrevSubKeyFile = curSubCert, curSubKey

	var warnings []string
	if cur == cfPanelPort {
		// 已经在目标端口上（例如用户手动改过），只补证书即可。
		st.PrevPort = 0
	} else {
		parked, movedTo, w, err := freePortForPanel()
		if err != nil {
			return st, nil, fmt.Errorf("腾出端口 %d 失败：%w", cfPanelPort, err)
		}
		st.ParkedInbound = parked
		st.ParkedPort = movedTo
		warnings = append(warnings, w...)
	}

	certFile := filepath.Join(certDir, "fullchain.pem")
	keyFile := filepath.Join(certDir, "privkey.pem")
	if err := m.settingService.SetPort(cfPanelPort); err != nil {
		return st, nil, fmt.Errorf("设置面板端口失败：%w", err)
	}
	if err := m.settingService.SetCertFile(certFile); err != nil {
		return st, nil, fmt.Errorf("设置面板证书失败：%w", err)
	}
	if err := m.settingService.SetKeyFile(keyFile); err != nil {
		return st, nil, fmt.Errorf("设置面板私钥失败：%w", err)
	}
	// 订阅服务也必须换成同一张域名证书：Cloudflare 把 2096 当作 HTTPS
	// 专用端口，回源时必定用 TLS 握手。订阅服务若还是明文 HTTP，CF 会
	// 直接返回「400 The plain HTTP request was sent to HTTPS port」，
	// 用户拿到的订阅链接就是死的。配上证书后 BuildSubURIBase 也会自动
	// 生成 https:// 链接，链路才真正通。
	if err := m.settingService.SetSubCertFile(certFile); err != nil {
		return st, nil, fmt.Errorf("设置订阅证书失败：%w", err)
	}
	if err := m.settingService.SetSubKeyFile(keyFile); err != nil {
		return st, nil, fmt.Errorf("设置订阅私钥失败：%w", err)
	}
	st.Moved = true

	if cur != cfPanelPort {
		warnings = append(warnings, fmt.Sprintf(
			"面板端口已从 %d 改为 %d —— 原端口不在 Cloudflare 代理的端口列表里，"+
				"不改的话开了域名也无法用域名登录面板", cur, cfPanelPort))
	}
	warnings = append(warnings, "面板将重启以在新端口生效；当前页面会断开，请改用新地址重新登录")
	return st, warnings, nil
}

// restorePanelPort 还原 movePanelToCFPort 做过的改动（幂等）。
func (m *Manager) restorePanelPort(prev panelPortState) {
	if prev.PrevPort > 0 {
		_ = m.settingService.SetPort(prev.PrevPort)
	}
	_ = m.settingService.SetCertFile(prev.PrevCertFile)
	_ = m.settingService.SetKeyFile(prev.PrevKeyFile)
	_ = m.settingService.SetSubCertFile(prev.PrevSubCertFile)
	_ = m.settingService.SetSubKeyFile(prev.PrevSubKeyFile)
	releaseParkedInbound(prev.ParkedInbound, prev.ParkedPort)
}

// restartPanelSoon 延迟重启面板，保证调用方的 HTTP 响应先写出去，
// 否则用户在端口切换的瞬间会拿到一个断掉的响应。
func restartPanelSoon() {
	_ = (&panel.PanelService{}).RestartPanel(3 * time.Second)
}

// currentBasePath 读取面板安全入口路径，失败时退回根路径。
func (m *Manager) currentBasePath() string {
	if bp, err := m.settingService.GetBasePath(); err == nil && strings.TrimSpace(bp) != "" {
		return bp
	}
	return "/"
}

// panelLoginURL 拼出启用域名后应该使用的面板登录地址。
func panelLoginURL(fqdn, basePath string) string {
	if fqdn == "" {
		return ""
	}
	bp := strings.TrimSpace(basePath)
	if bp == "" {
		bp = "/"
	}
	if !strings.HasPrefix(bp, "/") {
		bp = "/" + bp
	}
	if !strings.HasSuffix(bp, "/") {
		bp += "/"
	}
	return fmt.Sprintf("https://%s:%d%s", fqdn, cfPanelPort, bp)
}
