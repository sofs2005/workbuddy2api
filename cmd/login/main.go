// Command login — WorkBuddy CN OAuth 登录 → 落盘 auth 文件。
//
// 设计前提：宿主机不必装 Go / python3，整个流程在容器内一次跑完
// （state 只存活于进程内存，不再落 /tmp，因此 url 与 poll 不能拆成两次
// docker run —— 那会丢 state）。
//
//	docker run --rm -it -v "$PWD/auths:/app/auths" --user root \
//	  --entrypoint /app/login ghcr.io/sofs2005/workbuddy2api:latest
//
// 流程：
//  1. POST /v2/plugin/auth/state 拿授权 URL（无 PKCE，state 由服务端签发）
//  2. 你在浏览器打开 URL 完成登录
//  3. 回到这里按 y → poll 拿 token+uid+nickname → 签到 → 落盘 auths/workbuddy-<uid>.json
//  4. 重启 workbuddy2api 容器加载新账号
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"workbuddy2api/internal/auth"
	"workbuddy2api/internal/upstream"
)

// appUID 镜像内服务进程 uid（Dockerfile: adduser -u 10001 app）。
// 以 root 登录落盘时把文件 chown 给它，保证服务容器可读写 auths（token 续期要回写）。
const appUID = 10001

func main() {
	authDir := flag.String("auth-dir", envOr("AUTH_DIR", "/app/auths"), "auth 文件落盘目录")
	chownUID := flag.Int("chown-uid", appUID, "以 root 运行时把产物 chown 给该 uid；0 表示不 chown")
	flag.Parse()

	if err := run(*authDir, *chownUID); err != nil {
		fmt.Fprintf(os.Stderr, "login: %v\n", err)
		os.Exit(1)
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func run(authDir string, chownUID int) error {
	if err := os.MkdirAll(authDir, 0o755); err != nil {
		return fmt.Errorf("创建 auth 目录: %w", err)
	}

	client := newLoginClient()

	authURL, state, err := startAuth(client)
	if err != nil {
		return err
	}

	fmt.Println("============================================================")
	fmt.Println("  WorkBuddy OAuth 登录")
	fmt.Println("============================================================")
	fmt.Println()
	fmt.Println("请在浏览器中打开以下链接完成登录：")
	fmt.Println()
	fmt.Println("  " + authURL)
	fmt.Println()

	// 容器内无剪贴板，直接等用户在浏览器登录后回来确认
	fmt.Print("完成登录后按 y 继续: ")
	ans, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return fmt.Errorf("读取输入失败（需要 -it 交互模式）: %w", err)
	}
	ans = strings.ToLower(strings.TrimSpace(ans))
	if ans != "y" && ans != "yes" {
		return fmt.Errorf("已取消")
	}

	fmt.Println()
	fmt.Println("正在获取 token...")

	tb, err := pollAuth(client, state)
	if err != nil {
		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr, "获取 token 失败。可能原因：")
		fmt.Fprintln(os.Stderr, "  - 登录还没完成就按了 y（重新运行再试）")
		fmt.Fprintln(os.Stderr, "  - 登录页报错（把报错截图发出来排查）")
		return err
	}

	// ─── 签到（CN 幂等，失败不阻断登录）─────────────────────────
	doCheckin(tb)

	// ─── 落盘 auth 文件（与 internal/auth 读取格式一致）─────────────
	a := &auth.Auth{
		AccessToken:  tb.AccessToken,
		RefreshToken: tb.RefreshToken,
		ExpiresAt:    time.Now().Unix() + tb.ExpiresIn,
		Domain:       tb.Domain,
		UID:          tb.UID,
		EnterpriseID: tb.EnterpriseID,
		Nickname:     tb.Nickname,
	}
	file := filepath.Join(authDir, "workbuddy-"+tb.UID+".json")
	a.FilePath = file

	if _, statErr := os.Stat(file); statErr == nil {
		fmt.Printf("账号已存在（uid=%s），将覆盖更新凭证\n", tb.UID)
	} else {
		fmt.Printf("新账号（uid=%s），新增 auth 文件\n", tb.UID)
	}

	if err := a.SaveAtomic(); err != nil {
		return fmt.Errorf("写入 auth 文件: %w", err)
	}
	// 服务端 refresh 后要回写文件，权限必须是服务进程可读可写
	if err := os.Chmod(file, 0o600); err != nil {
		return fmt.Errorf("chmod auth 文件: %w", err)
	}
	if err := chownToService(file, authDir, chownUID); err != nil {
		return err
	}

	fmt.Printf("已保存: %s\n", file)
	fmt.Println()
	fmt.Println("============================================================")
	fmt.Println("  登录完成！")
	fmt.Println("  UID:      " + tb.UID)
	fmt.Println("  Nickname: " + displayOr(tb.Nickname, "（未获取到）"))
	fmt.Println("  Token:    " + truncate(tb.AccessToken, 30) + "...")
	fmt.Println("  有效期:   " + time.Unix(a.ExpiresAt, 0).Format("2006-01-02 15:04"))
	fmt.Println("============================================================")
	fmt.Println()
	fmt.Println("重启服务加载新账号：docker compose restart")
	return nil
}

// doCheckin 首次签到（幂等）。失败仅提示，不阻断——调度器每日 09:00/21:00 会重试。
func doCheckin(tb tokenBundle) {
	a := &auth.Auth{
		AccessToken:  tb.AccessToken,
		RefreshToken: tb.RefreshToken,
		Domain:       tb.Domain,
		UID:          tb.UID,
		EnterpriseID: tb.EnterpriseID,
		Nickname:     tb.Nickname,
	}
	if err := upstream.New().DailyCheckin(a); err != nil {
		fmt.Printf("签到: 失败（%v）—— 调度器会于 09:00/21:00 重试\n", err)
		return
	}
	fmt.Println("签到: 成功")
}

// chownToService 以 root 运行时把产物归属改给服务进程，避免宿主目录属主
// 与服务容器 uid 不一致导致 token 续期回写失败。
func chownToService(file, dir string, uid int) error {
	if uid <= 0 || os.Geteuid() != 0 {
		return nil
	}
	if err := os.Chown(dir, uid, uid); err != nil {
		return fmt.Errorf("chown auth 目录: %w", err)
	}
	if err := os.Chown(file, uid, uid); err != nil {
		return fmt.Errorf("chown auth 文件: %w", err)
	}
	fmt.Printf("已将 %s 归属改为 uid=%s（服务进程）\n", dir, strconv.Itoa(uid))
	return nil
}

func displayOr(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return s[:n]
	}
	return s
}
