package controller

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-contrib/sessions"
	"github.com/gin-contrib/sessions/cookie"
	"github.com/gin-gonic/gin"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/session"
)

// newCardKeyAPITestEngine wires the clients API the way the panel does — the
// session store, then checkAPIAuth guarding NewClientController on
// /panel/api/clients — so a route conflict among the existing /:email routes
// and the new /cardkeys routes would panic here, not on a live panel.
func newCardKeyAPITestEngine(t *testing.T) (*gin.Engine, *model.Inbound) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	dbDir := t.TempDir()
	t.Setenv("XUI_DB_FOLDER", dbDir)
	if err := database.InitDB(filepath.Join(dbDir, "x-ui.db")); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	t.Cleanup(func() { _ = database.CloseDB() })

	db := database.GetDB()
	inbound := &model.Inbound{
		UserId:         1,
		Tag:            "cardkey-api",
		Enable:         true,
		Port:           8443,
		Protocol:       model.VLESS,
		Settings:       `{"clients":[]}`,
		StreamSettings: `{"network":"tcp","security":"none"}`,
	}
	if err := db.Create(inbound).Error; err != nil {
		t.Fatalf("seed inbound: %v", err)
	}

	engine := gin.New()
	engine.Use(sessions.Sessions("3x-ui", cookie.NewStore([]byte("cardkey-api-test"))))
	api := &APIController{}
	engine.GET("/test-login", func(c *gin.Context) {
		user, err := api.userService.GetFirstUser()
		if err != nil {
			c.Status(http.StatusInternalServerError)
			return
		}
		if err := session.SetLoginUser(c, user); err != nil {
			c.Status(http.StatusInternalServerError)
			return
		}
		c.Status(http.StatusOK)
	})
	clients := engine.Group("/panel/api/clients")
	clients.Use(api.checkAPIAuth)
	NewClientController(clients)
	return engine, inbound
}

// cardKeyAPIClient logs in once and replays the session cookie on every call,
// mirroring the browser's XHR traffic.
type cardKeyAPIClient struct {
	t      *testing.T
	engine *gin.Engine
	cookie string
}

func newCardKeyAPIClient(t *testing.T, engine *gin.Engine) *cardKeyAPIClient {
	t.Helper()
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/test-login", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("test login = %d, want 200", rec.Code)
	}
	cookie := rec.Header().Get("Set-Cookie")
	if cookie == "" {
		t.Fatal("test login set no session cookie")
	}
	return &cardKeyAPIClient{t: t, engine: engine, cookie: cookie}
}

func (c *cardKeyAPIClient) do(method, path, body string) *httptest.ResponseRecorder {
	c.t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Cookie", c.cookie)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	c.engine.ServeHTTP(rec, req)
	return rec
}

type cardKeyEnvelope struct {
	Success bool   `json:"success"`
	Msg     string `json:"msg"`
	Obj     struct {
		Created int `json:"created"`
		Keyed   int `json:"keyed"`
		Keys    []struct {
			CardKey    string `json:"cardKey"`
			Email      string `json:"email"`
			SubID      string `json:"subId"`
			InboundID  int    `json:"inboundId"`
			TotalGB    int64  `json:"totalGB"`
			ExpiryTime int64  `json:"expiryTime"`
			Enable     bool   `json:"enable"`
		} `json:"keys"`
		Skipped []struct {
			Email  string `json:"email"`
			Reason string `json:"reason"`
		} `json:"skipped"`
	} `json:"obj"`
}

func decodeCardKeyEnvelope(t *testing.T, rec *httptest.ResponseRecorder) cardKeyEnvelope {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	var envelope cardKeyEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode envelope %q: %v", rec.Body.String(), err)
	}
	return envelope
}

func TestGenerateCardKeysAPIReturnsKeysInTheStandardEnvelope(t *testing.T) {
	engine, inbound := newCardKeyAPITestEngine(t)
	client := newCardKeyAPIClient(t, engine)

	body := fmt.Sprintf(`{"inboundId":%d,"count":2,"totalGB":%d,"expiryDays":7,"remarkPrefix":"api","groupName":"g1"}`,
		inbound.Id, int64(1)<<30)
	envelope := decodeCardKeyEnvelope(t, client.do(http.MethodPost, "/panel/api/clients/cardkeys/generate", body))
	if !envelope.Success {
		t.Fatalf("generate failed: %s", envelope.Msg)
	}
	if envelope.Obj.Created != 2 || envelope.Obj.Keyed != 2 || len(envelope.Obj.Keys) != 2 || len(envelope.Obj.Skipped) != 0 {
		t.Fatalf("envelope = %+v, want 2 created, 2 keyed, 2 keys, no skipped", envelope.Obj)
	}
	for i, key := range envelope.Obj.Keys {
		if !strings.HasPrefix(key.CardKey, "LD-") || len(key.CardKey) != len("LD-XXXX-XXXX-XXXX") {
			t.Fatalf("key %d = %q, want the LD-XXXX-XXXX-XXXX display form", i, key.CardKey)
		}
		if key.Email != []string{"api0001", "api0002"}[i] {
			t.Fatalf("key %d email = %q", i, key.Email)
		}
		if key.InboundID != inbound.Id || key.TotalGB != int64(1)<<30 || !key.Enable {
			t.Fatalf("key %d = %+v, want the requested inbound, quota and enabled state", i, key)
		}
		var stored model.ClientRecord
		if err := database.GetDB().Where("email = ?", key.Email).First(&stored).Error; err != nil {
			t.Fatalf("client %q was not created: %v", key.Email, err)
		}
		if stored.CardKey == nil || stored.SubID != key.SubID {
			t.Fatalf("stored client %+v does not match the reported key %+v", stored, key)
		}
	}
}

func TestGenerateCardKeysAPIRejectsBadRequests(t *testing.T) {
	engine, inbound := newCardKeyAPITestEngine(t)
	client := newCardKeyAPIClient(t, engine)

	for _, body := range []string{
		`{"count":1}`,
		`{"inboundId":9999,"count":1}`,
		fmt.Sprintf(`{"inboundId":%d,"count":0}`, inbound.Id),
		fmt.Sprintf(`{"inboundId":%d,"count":501}`, inbound.Id),
		`not json`,
	} {
		envelope := decodeCardKeyEnvelope(t, client.do(http.MethodPost, "/panel/api/clients/cardkeys/generate", body))
		if envelope.Success {
			t.Fatalf("body %s was accepted: %+v", body, envelope.Obj)
		}
		if len(envelope.Obj.Keys) != 0 {
			t.Fatalf("body %s returned keys after a rejection", body)
		}
	}
}

func TestExportCardKeysAPIReturnsACSVDownload(t *testing.T) {
	engine, inbound := newCardKeyAPITestEngine(t)
	client := newCardKeyAPIClient(t, engine)

	body := fmt.Sprintf(`{"inboundId":%d,"count":2,"remarkPrefix":"csv","groupName":"g1"}`, inbound.Id)
	if envelope := decodeCardKeyEnvelope(t, client.do(http.MethodPost, "/panel/api/clients/cardkeys/generate", body)); !envelope.Success {
		t.Fatalf("seed generate failed: %s", envelope.Msg)
	}
	// A client with no card key must still be listed.
	if err := database.GetDB().Create(&model.ClientRecord{Email: "nokey@x", SubID: "sub-nokey", Enable: true}).Error; err != nil {
		t.Fatalf("seed keyless client: %v", err)
	}

	rec := client.do(http.MethodGet, fmt.Sprintf("/panel/api/clients/cardkeys/export?inboundId=%d", inbound.Id), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("export status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/csv") {
		t.Fatalf("export content type = %q, want text/csv", ct)
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, fmt.Sprintf("cardkeys-inbound-%d.csv", inbound.Id)) {
		t.Fatalf("export disposition = %q", cd)
	}
	raw := rec.Body.String()
	if !strings.HasPrefix(raw, "\ufeff") {
		t.Fatal("export is missing the UTF-8 BOM")
	}
	records, err := csv.NewReader(strings.NewReader(strings.TrimPrefix(raw, "\ufeff"))).ReadAll()
	if err != nil {
		t.Fatalf("parse exported CSV: %v", err)
	}
	// The keyless client is not attached to the inbound, so the filtered export
	// holds the header plus the two generated clients.
	if len(records) != 3 {
		t.Fatalf("CSV lines = %d, want 3 (%v)", len(records), records)
	}
	if strings.Join(records[0], ",") != "卡密,邮箱,备注,流量GB,到期时间,已用上传,已用下载,启用,入站端口" {
		t.Fatalf("CSV header = %v", records[0])
	}
	for _, row := range records[1:] {
		if len(row) != 9 {
			t.Fatalf("CSV row %v has %d columns, want 9", row, len(row))
		}
		if !strings.HasPrefix(row[0], "LD-") {
			t.Fatalf("CSV row %v does not carry a display card key", row)
		}
		if row[8] != fmt.Sprintf("%d", inbound.Port) {
			t.Fatalf("CSV row %v port column = %q, want %d", row, row[8], inbound.Port)
		}
	}

	// Unfiltered export includes the client without a key, marked 未生成.
	all := client.do(http.MethodGet, "/panel/api/clients/cardkeys/export", "")
	allRecords, err := csv.NewReader(strings.NewReader(strings.TrimPrefix(all.Body.String(), "\ufeff"))).ReadAll()
	if err != nil {
		t.Fatalf("parse unfiltered CSV: %v", err)
	}
	foundKeyless := false
	for _, row := range allRecords[1:] {
		if row[1] == "nokey@x" {
			foundKeyless = true
			if row[0] != "未生成" {
				t.Fatalf("keyless row card key = %q, want 未生成", row[0])
			}
		}
	}
	if !foundKeyless {
		t.Fatalf("unfiltered export dropped the client without a card key: %v", allRecords)
	}

	// A malformed inboundId is a JSON error, not a broken download.
	bad := decodeCardKeyEnvelope(t, client.do(http.MethodGet, "/panel/api/clients/cardkeys/export?inboundId=abc", ""))
	if bad.Success {
		t.Fatal("export accepted inboundId=abc")
	}
}

// Both endpoints are admin-only: without a session (or an admin API token) they
// must not answer at all.
func TestCardKeyAPIsRequireAuthentication(t *testing.T) {
	engine, inbound := newCardKeyAPITestEngine(t)
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodPost, "/panel/api/clients/cardkeys/generate", fmt.Sprintf(`{"inboundId":%d,"count":1}`, inbound.Id)},
		{http.MethodGet, "/panel/api/clients/cardkeys/export", ""},
	} {
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound && rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s without a session = %d, want 404 or 401", tc.method, tc.path, rec.Code)
		}
	}
	var count int64
	if err := database.GetDB().Model(&model.ClientRecord{}).Count(&count).Error; err != nil {
		t.Fatalf("count clients: %v", err)
	}
	if count != 0 {
		t.Fatalf("an unauthenticated call created %d clients", count)
	}
}
