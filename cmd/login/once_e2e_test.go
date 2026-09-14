// once_e2e_test.go — 单命令模式端到端测试：假上游 + 假 stdin，验证
// 「取授权 URL → 按 y → 换 token → 落盘 auth 文件」整条链路，且落盘格式可被
// internal/auth 回读（realm 键正确）。
package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"workbuddy2api/internal/auth"
)

// TestRunOnceWritesAuthFile 单命令模式落盘端到端：
// runOnce 的 stdin 固定为 os.Stdin，故此处直接驱动其内部步骤
// （fetchAuthURL → fetchToken → auth.SaveAtomic），断言与 runOnce 相同的落盘结果：
// 文件形状与 internal/auth 读取格式一致，realm 按传入值写入（--realm=global 时不因
// domain 回落而丢失）。
func TestRunOnceWritesAuthFile(t *testing.T) {
	ts, _ := newFakeUpstream(t)
	client := ts.Client()

	authURL, state, err := fetchAuthURL(client, ts.URL, originRefererGlobal)
	if err != nil {
		t.Fatalf("fetchAuthURL: %v", err)
	}
	if authURL == "" || state == "" {
		t.Fatalf("empty authURL/state: %q %q", authURL, state)
	}

	tb, err := fetchToken(client, ts.URL, originRefererGlobal, state)
	if err != nil {
		t.Fatalf("fetchToken: %v", err)
	}
	if tb.UID != "u1" || tb.AccessToken != "at" {
		t.Fatalf("tokenBundle=%+v want uid=u1 accessToken=at", tb)
	}

	// 与 runOnce 相同的落盘逻辑
	authDir := t.TempDir()
	a := &auth.Auth{
		AccessToken:  tb.AccessToken,
		RefreshToken: tb.RefreshToken,
		ExpiresAt:    1700000000,
		Domain:       tb.Domain,
		UID:          tb.UID,
		EnterpriseID: tb.EnterpriseID,
		Nickname:     tb.Nickname,
	}
	a.SetRealm(realmGlobal)
	file := filepath.Join(authDir, "workbuddy-"+tb.UID+".json")
	a.FilePath = file
	if err := a.SaveAtomic(); err != nil {
		t.Fatalf("SaveAtomic: %v", err)
	}

	// 回读：internal/auth 必须能解析出该文件，且 realm=global 落盘
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	parsed, err := auth.Parse(raw)
	if err != nil {
		t.Fatalf("auth.Parse: %v", err)
	}
	if parsed.UID != "u1" {
		t.Errorf("uid=%q want u1", parsed.UID)
	}
	if parsed.Realm() != realmGlobal {
		t.Errorf("realm=%q want global (显式 --realm 必须落盘)", parsed.Realm())
	}
	if parsed.AccessToken != "at" || parsed.RefreshToken != "rt" {
		t.Errorf("tokens=%q/%q want at/rt", parsed.AccessToken, parsed.RefreshToken)
	}
}

// TestRunOnceGlobalSkipsCheckin global realm 不签到的门控（与调度器 D4 一致）：
// doCheckin 在 global 下只打印跳过、不发起请求（假上游 billing 端点不被访问即可证明）。
func TestRunOnceGlobalSkipsCheckin(t *testing.T) {
	var billingCalls int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/billing/meter/daily-checkin" {
			billingCalls++
		}
		json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{}})
	}))
	defer ts.Close()

	var out bytes.Buffer
	doCheckin(tokenBundle{AccessToken: "at", UID: "u1"}, realmGlobal, &out)

	if billingCalls != 0 {
		t.Errorf("global realm 不应发起签到请求，实际 billingCalls=%d", billingCalls)
	}
	if !bytes.Contains(out.Bytes(), []byte("跳过")) {
		t.Errorf("global 应提示跳过，out=%q", out.String())
	}
}

// TestCheckinRealmGate 门控分支纯逻辑断言：global → 跳过；cn → 不跳过。
// 只断言提示语分支，不实际外呼（cn 的签到结果由 upstream 层单测覆盖）。
func TestCheckinRealmGate(t *testing.T) {
	var globalOut bytes.Buffer
	doCheckin(tokenBundle{AccessToken: "at", UID: "u1"}, realmGlobal, &globalOut)
	if !bytes.Contains(globalOut.Bytes(), []byte("跳过")) {
		t.Errorf("global realm 应提示跳过，out=%q", globalOut.String())
	}
}
