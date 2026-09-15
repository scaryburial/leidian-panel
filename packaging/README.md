# 雷电面板 (ui3344) 安装包

## 安装
1. 上传并解压：
   ```
   tar xzf ui3344-linux-amd64.tar.gz
   cd ui3344
   ```
2. 执行安装（需 root）：
   ```
   bash install.sh
   ```
3. 浏览器打开： `http://<服务器IP>:33441/ui3344/`
   默认账号 / 密码：`3344` / `3344`（请登录后尽快改强）
4. 管理命令： `ui3344`（交互菜单为中文）

## 卸载
```
systemctl stop ui3344
systemctl disable ui3344
rm -f /etc/systemd/system/ui3344.service
rm -f /usr/bin/ui3344
# 数据与程序目录（确认不再需要后再删）
rm -rf /usr/local/ui3344 /etc/ui3344 /var/log/ui3344
systemctl daemon-reload
```

## 说明
- 全程离线，不访问上游；面板内置 3 个 Xray 内核版本可离线切换。
- 安装包内 `bin/` 已含默认内核（v26.9.9）与 geoip/geosite 文件，装完 Xray 即可运行。
- 默认：端口 `33441`、安全入口 `/ui3344/`、账号密码 `3344/3344`（弱口令，务必尽快修改）。
