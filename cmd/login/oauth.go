// oauth.go — OAuth 设备流的三个原子步骤（url / poll / 端点常量）。
//
// 与 /root/qoderwork/workbuddy/oauth.go 的 handleStartLogin + handlePollLogin
// 逐字一致的实现，CN realm only。
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"time"
)

// 与 /root/qoderwork/workbuddy/main.go:82-96 完全一致的常量（CN only）
const (
	upstreamBaseCN    = "https://copilot.tencent.com"
	clientUA          = "CLI/2.63.2 CodeBuddy/2.63.2"
	originReferer     = "https://www.codebuddy.cn"
	endpointAuthState = upstreamBaseCN + "/v2/plugin/auth/state?platform=CLI"
	endpointLoginAcct = upstreamBaseCN + "/v2/plugin/login/account?state="
	endpointAuthToken = upstreamBaseCN + "/v2/plugin/auth/token?state="
)

// newLoginClient 每个流程独立 cookie jar（oauth.go:22-29：多账号登录互不串会话）
func newLoginClient() *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Timeout: 30 * time.Second, Jar: jar}
}

// commonHeaders 与 main.go:496-503 一致
func commonHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("Origin", originReferer)
	req.Header.Set("Referer", originReferer+"/")
	req.Header.Set("User-Agent", clientUA)
}

// apiEnvelope 与 main.go:429-433 一致
type apiEnvelope struct {
	Code int             `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

// doJSON 与 oauth.go:33-66 一致：{code,msg,data} 信封，code!=0 → error
func doJSON(client *http.Client, method, fullURL string, headers func(*http.Request), body io.Reader) (json.RawMessage, int, error) {
	req, err := http.NewRequest(method, fullURL, body)
	if err != nil {
		return nil, 0, err
	}
	if headers != nil {
		headers(req)
	} else {
		commonHeaders(req)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, resp.StatusCode, fmt.Errorf("http_error: upstream %d", resp.StatusCode)
	}
	if resp.StatusCode >= 300 {
		return nil, resp.StatusCode, fmt.Errorf("http_error: upstream redirect %d", resp.StatusCode)
	}
	var env apiEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, resp.StatusCode, fmt.Errorf("parse failed: %w", err)
	}
	if env.Code != 0 {
		return nil, resp.StatusCode, fmt.Errorf("code=%d msg=%s", env.Code, env.Msg)
	}
	return env.Data, resp.StatusCode, nil
}

// startAuth 发起授权（handleStartLogin, oauth.go:68-87），返回授权 URL 与 state
func startAuth(client *http.Client) (authURL, state string, err error) {
	data, _, err := doJSON(client, http.MethodPost, endpointAuthState, nil, bytes.NewReader([]byte("{}")))
	if err != nil {
		return "", "", fmt.Errorf("auth state failed: %w", err)
	}
	var st struct {
		State   string `json:"state"`
		AuthURL string `json:"authUrl"`
	}
	if err := json.Unmarshal(data, &st); err != nil || st.State == "" || st.AuthURL == "" {
		return "", "", fmt.Errorf("auth state: missing state or authUrl")
	}
	return st.AuthURL, st.State, nil
}

// tokenBundle poll 成功后拿到的凭证
type tokenBundle struct {
	AccessToken  string
	RefreshToken string
	ExpiresIn    int64
	Domain       string
	UID          string
	EnterpriseID string
	Nickname     string
}

// pollAuth 换取 token（handlePollLogin, oauth.go:108-162）。
// auth/token 是权威登录状态端点，pending 时业务 code 非 0（"login ing"）。
func pollAuth(client *http.Client, state string) (tokenBundle, error) {
	var out tokenBundle
	tokRaw, status, errTok := doJSON(client, http.MethodGet, endpointAuthToken+state, nil, nil)
	if errTok != nil {
		if status == 0 || status >= 500 {
			return out, fmt.Errorf("token endpoint error: %w", errTok)
		}
		return out, fmt.Errorf("登录未完成（waiting for login）")
	}
	var tok struct {
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken"`
		ExpiresIn    int64  `json:"expiresIn"`
		Domain       string `json:"domain"`
	}
	if err := json.Unmarshal(tokRaw, &tok); err != nil || tok.AccessToken == "" {
		return out, fmt.Errorf("登录未完成（waiting for login）")
	}
	out.AccessToken = tok.AccessToken
	out.RefreshToken = tok.RefreshToken
	out.ExpiresIn = tok.ExpiresIn
	out.Domain = tok.Domain

	// login/account 拿 uid/nickname（带 Bearer）；失败不阻断——uid 是必需的，下方校验
	var acct struct {
		UID          string `json:"uid"`
		EnterpriseID string `json:"enterpriseId"`
		Nickname     string `json:"nickname"`
	}
	acctHeaders := func(r *http.Request) {
		commonHeaders(r)
		r.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	}
	if acctRaw, _, errAcct := doJSON(client, http.MethodGet, endpointLoginAcct+state, acctHeaders, nil); errAcct == nil {
		_ = json.Unmarshal(acctRaw, &acct)
	}
	out.UID = acct.UID
	out.EnterpriseID = acct.EnterpriseID
	out.Nickname = acct.Nickname
	if out.UID == "" {
		return out, fmt.Errorf("无法获取 uid，请检查 token 是否有效")
	}
	return out, nil
}
