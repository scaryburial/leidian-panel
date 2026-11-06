// 命令 syncpreferred：在不改动面板任何状态的前提下，重建「CF优选域名」组。
//
// 面板的域名功能只在 Enable 时生成一次优选组；如果之后才补建预设入站（或入站
// 的 WS+TLS 配置才到位），组里就会一直是空的。而 Disable→Enable 会删 DNS 记录、
// 还原面板端口、重启进程，代价太大。这个工具直接调同一份 SyncPreferredHosts，
// 只重写 hosts 表里那个组，不动域名状态与入站。
//
// 用法（容器内，需能读到面板数据库）：
//
//	XUI_DB_FOLDER=/etc/ui3344 syncpreferred
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/config"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service/domain"
)

func main() {
	dbPath := config.GetDBPath()
	if err := database.InitDB(dbPath); err != nil {
		fmt.Fprintf(os.Stderr, "打开数据库失败 %s：%v\n", dbPath, err)
		os.Exit(1)
	}

	// 面板把当前域名存在 settings.subDomain 里（启用域名功能时写入）。
	svc := &service.SettingService{}
	fqdn, err := svc.GetSubDomain()
	if err != nil || fqdn == "" {
		fmt.Fprintln(os.Stderr, "尚未启用域名功能（settings.subDomain 为空），无需生成优选域名。")
		os.Exit(2)
	}
	fmt.Printf("域名：%s\n", fqdn)

	inbounds, err := domain.EligiblePreferredInbounds()
	if err != nil {
		fmt.Fprintf(os.Stderr, "读取入站失败：%v\n", err)
		os.Exit(1)
	}
	fmt.Printf("可用于优选域名的入站：%d 个\n", len(inbounds))
	for _, ib := range inbounds {
		fmt.Printf("  · %-24s :%-5d %-8s path=%s\n", ib.Remark, ib.Port, ib.Protocol, ib.Path)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	domains, rejected, warnings, err := domain.SyncPreferredHosts(ctx, fqdn, domain.PreferredDomainCandidates)
	if err != nil {
		fmt.Fprintf(os.Stderr, "同步失败：%v\n", err)
		os.Exit(1)
	}
	for _, w := range warnings {
		fmt.Println("提示：" + w)
	}
	fmt.Printf("通过校验的第三方优选域名：%d 个\n", len(domains))
	for _, d := range domains {
		fmt.Println("  · " + d)
	}
	fmt.Printf("被剔除：%d 个\n", len(rejected))
	for _, r := range rejected {
		fmt.Printf("  ✗ %-32s %s\n", r.Domain, r.Reason)
	}
}
