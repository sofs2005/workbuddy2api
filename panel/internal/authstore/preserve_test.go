package authstore

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestSavePreservesUnmodeledKeys 是本包最重要的一条回归：
// 面板点「刷新」→ refreshAccount → store.Save 会整份重建凭证文档并覆盖原文件。
// 若不保留未建模的键，网关写入的 auth.realm 与顶层 device_token 会被抹掉，
// 其中 device_token **不可反推**（丢失即永久失去 X-Device-Token 风控头）。
func TestSavePreservesUnmodeledKeys(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	uid := "11111111-2222-3333-4444-555555555555"
	path := filepath.Join(dir, "workbuddy-"+uid+".json")
	// 模拟网关 SaveAtomic 的产物：含 realm 与顶层 device_token。
	original := `{
	  "auth": {
	    "accessToken": "old-at",
	    "refreshToken": "old-rt",
	    "expiresAt": 100,
	    "domain": "www.workbuddy.ai",
	    "realm": "global"
	  },
	  "account": {"uid": "` + uid + `", "enterpriseId": "ent-1", "nickname": "旧名"},
	  "device_token": "dt-secret-value"
	}`
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// 面板只认识部分字段 —— 这正是 Parse 的结果。
	a, err := Parse([]byte(original))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	// 模拟刷新：token 变化后再 Save。
	a.AccessToken = "new-at"
	a.RefreshToken = "new-rt"
	a.ExpiresAt = 200
	if err := s.Save(a); err != nil {
		t.Fatalf("Save: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("解析写回结果: %v", err)
	}

	// 未建模的键必须原样保留。
	if dt, _ := got["device_token"].(string); dt != "dt-secret-value" {
		t.Errorf("device_token 丢失或被改：got %v，want %q", got["device_token"], "dt-secret-value")
	}
	authMap, _ := got["auth"].(map[string]any)
	if authMap == nil {
		t.Fatal("auth 段丢失")
	}
	if realm, _ := authMap["realm"].(string); realm != "global" {
		t.Errorf("auth.realm 丢失或被改：got %v，want %q", authMap["realm"], "global")
	}

	// 面板管理的字段必须按新值写入（旧值不得回滚覆盖）。
	if at, _ := authMap["accessToken"].(string); at != "new-at" {
		t.Errorf("accessToken 未更新：got %v", authMap["accessToken"])
	}
	if exp, _ := authMap["expiresAt"].(float64); exp != 200 {
		t.Errorf("expiresAt 未更新：got %v", authMap["expiresAt"])
	}
}

// TestSaveWithoutExistingFile 确认新建账号（无旧文件）不会因保留逻辑报错。
func TestSaveWithoutExistingFile(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	a := &Account{
		AccessToken:  "at",
		RefreshToken: "rt",
		ExpiresAt:    123,
		UID:          "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
		Nickname:     "新号",
	}
	if err := s.Save(a); err != nil {
		t.Fatalf("Save: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "workbuddy-"+a.UID+".json"))
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("解析: %v", err)
	}
	if _, ok := got["auth"]; !ok {
		t.Error("auth 段缺失")
	}
}
