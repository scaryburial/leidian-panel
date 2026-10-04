package service

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/util/cardkey"
	"github.com/mhsanaei/3x-ui/v3/internal/util/common"
)

// Card keys ("卡密") are an alternative handle for a client's subscription:
// instead of handing a customer the subId, the operator hands out
// LD-XXXX-XXXX-XXXX, which GET /c/<card_key> resolves back to the same client.
// The key lives in clients.card_key (unique, nullable), so a client may exist
// without one — the panel's own list marks those "未生成".
//
// Everything here is deliberately layered on top of the existing client CRUD
// and subscription code: GenerateCardKeys creates clients through BulkCreate
// (so inbounds, normalised tables, hot reload and node fan-out behave exactly
// as an ordinary create), and the subscription side resolves a key to a subId
// and then runs the untouched subscription code path.

const (
	// DefaultCardKeyPrefix is the email / remark prefix used when a caller
	// does not supply one.
	DefaultCardKeyPrefix = "card"
	// MaxCardKeyBatch caps a single generate call. Each client costs an
	// inbound settings rewrite, so an unbounded batch would stall the panel.
	MaxCardKeyBatch = 500
	// cardKeySequenceWidth zero-pads the per-batch sequence number so that
	// listing card-key clients keeps creation order (card0002 before card0010).
	cardKeySequenceWidth = 4
	// cardKeyGB is the byte count one "GB" means everywhere in this panel.
	cardKeyGB = 1 << 30
)

// GetSubIDByCardKey resolves a card key in any accepted input form
// (LD-XXXX-XXXX-XXXX, bare 12 characters, lowercase, O/I/L confusables) to the
// sub_id owned by that client.
//
// A malformed key and an unknown key are both reported as found=false: the
// subscription endpoint must not tell a caller which of the two it was, and
// neither ever reaches the database in the malformed case.
func (s *ClientService) GetSubIDByCardKey(key string) (string, bool, error) {
	canonical, ok := cardkey.Parse(key)
	if !ok {
		return "", false, nil
	}
	var rec model.ClientRecord
	err := database.GetDB().
		Model(&model.ClientRecord{}).
		Select("sub_id").
		Where("card_key = ?", canonical).
		First(&rec).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	subID := strings.TrimSpace(rec.SubID)
	if subID == "" {
		// A client without a subscription id has no subscription to serve.
		return "", false, nil
	}
	return subID, true, nil
}

// CardKeyLookup returns the client record owning a card key, for callers that
// need more than the sub_id (the panel API shows the key on a client row).
func (s *ClientService) CardKeyLookup(key string) (*model.ClientRecord, error) {
	canonical, ok := cardkey.Parse(key)
	if !ok {
		return nil, common.NewError("not a valid card key:", key)
	}
	rec := &model.ClientRecord{}
	err := database.GetDB().Model(&model.ClientRecord{}).Where("card_key = ?", canonical).First(rec).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, common.NewError("card key not found:", cardkey.Format(canonical))
	}
	if err != nil {
		return nil, err
	}
	return rec, nil
}

// CardKeyGenerateParams is the accepted body of the batch generate API.
type CardKeyGenerateParams struct {
	InboundID    int    `json:"inboundId"`
	Count        int    `json:"count"`
	TotalGB      int64  `json:"totalGB"`
	ExpiryDays   int    `json:"expiryDays"`
	RemarkPrefix string `json:"remarkPrefix"`
	GroupName    string `json:"groupName"`
}

// GeneratedCardKey is one card key and the client it belongs to.
type GeneratedCardKey struct {
	CardKey    string `json:"cardKey"`
	Email      string `json:"email"`
	SubID      string `json:"subId"`
	InboundID  int    `json:"inboundId"`
	TotalGB    int64  `json:"totalGB"`
	ExpiryTime int64  `json:"expiryTime"`
	Enable     bool   `json:"enable"`
}

// CardKeyGenerateResult reports a batch. Created counts the clients the bulk
// create committed; Keyed counts those that also got their card key stored
// (len(Keys)). Anything short of both appears in Skipped with the reason, so a
// partial batch is never silently reported as a success.
type CardKeyGenerateResult struct {
	Created int                `json:"created"`
	Keyed   int                `json:"keyed"`
	Keys    []GeneratedCardKey `json:"keys"`
	Skipped []BulkCreateReport `json:"skipped,omitempty"`
}

// GenerateCardKeys creates Count clients on InboundID through the ordinary
// bulk-create path and assigns each a fresh card key.
func (s *ClientService) GenerateCardKeys(inboundSvc *InboundService, params CardKeyGenerateParams) (CardKeyGenerateResult, bool, error) {
	result := CardKeyGenerateResult{Keys: []GeneratedCardKey{}}
	if params.InboundID <= 0 {
		return result, false, common.NewError("inboundId is required")
	}
	if params.Count <= 0 {
		return result, false, common.NewError("count must be greater than 0")
	}
	if params.Count > MaxCardKeyBatch {
		return result, false, common.NewError("count exceeds the per-request limit of", MaxCardKeyBatch)
	}
	if params.TotalGB < 0 {
		return result, false, common.NewError("totalGB must not be negative")
	}
	if params.ExpiryDays < 0 {
		return result, false, common.NewError("expiryDays must not be negative")
	}
	if _, err := inboundSvc.GetInbound(params.InboundID); err != nil {
		return result, false, fmt.Errorf("inbound %d: %w", params.InboundID, err)
	}

	prefix := strings.TrimSpace(params.RemarkPrefix)
	if prefix == "" {
		prefix = DefaultCardKeyPrefix
	}
	emails, err := s.allocateCardKeyEmails(prefix, params.Count)
	if err != nil {
		return result, false, err
	}
	keys := newCardKeyBatch(params.Count)

	now := time.Now().UnixMilli()
	var expiry int64
	if params.ExpiryDays > 0 {
		expiry = now + int64(params.ExpiryDays)*int64((24*time.Hour)/time.Millisecond)
	}

	payloads := make([]ClientCreatePayload, 0, len(emails))
	for _, email := range emails {
		payloads = append(payloads, ClientCreatePayload{
			Client: model.Client{
				Email:      email,
				SubID:      uuid.NewString(),
				TotalGB:    params.TotalGB,
				ExpiryTime: expiry,
				Enable:     true,
				Comment:    prefix,
				Group:      strings.TrimSpace(params.GroupName),
				CreatedAt:  now,
				UpdatedAt:  now,
			},
			InboundIds: []int{params.InboundID},
		})
	}

	createResult, needRestart, err := s.BulkCreate(inboundSvc, payloads)
	if err != nil {
		return result, needRestart, err
	}
	result.Created = createResult.Created

	skipped := make(map[string]string, len(createResult.Skipped))
	for _, sk := range createResult.Skipped {
		skipped[strings.ToLower(sk.Email)] = sk.Reason
	}

	for i, email := range emails {
		if reason, bad := skipped[strings.ToLower(email)]; bad {
			result.Skipped = append(result.Skipped, BulkCreateReport{Email: email, Reason: reason})
			continue
		}
		stored, storeErr := s.storeCardKeyForEmail(email, keys[i])
		if storeErr != nil {
			// The client exists but has no key: say so instead of reporting a
			// card the operator cannot hand out. The CSV export lists it as
			// 未生成, and a later generate/rerun can fill it in.
			result.Skipped = append(result.Skipped, BulkCreateReport{
				Email:  email,
				Reason: "client created but its card key was not stored: " + storeErr.Error(),
			})
			continue
		}
		result.Keys = append(result.Keys, GeneratedCardKey{
			CardKey:    cardkey.Format(stored),
			Email:      email,
			SubID:      payloads[i].Client.SubID,
			InboundID:  params.InboundID,
			TotalGB:    params.TotalGB,
			ExpiryTime: expiry,
			Enable:     true,
		})
	}
	result.Keyed = len(result.Keys)
	return result, needRestart, nil
}

// newCardKeyBatch mints count distinct keys. Collisions are impossible in
// practice (55 random bits) but the batch itself is checked so a caller can
// never receive the same card twice from one call.
func newCardKeyBatch(count int) []string {
	keys := make([]string, 0, count)
	seen := make(map[string]struct{}, count)
	for len(keys) < count {
		key := cardkey.Generate()
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		keys = append(keys, key)
	}
	return keys
}

// storeCardKeyForEmail writes the canonical key onto the client's row and
// returns the key actually stored. The column is unique, so a (vanishingly
// unlikely) collision with a key handed out earlier is retried with a fresh
// key rather than failing the whole batch.
func (s *ClientService) storeCardKeyForEmail(email, key string) (string, error) {
	db := database.GetDB()
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		res := db.Model(&model.ClientRecord{}).
			Where("email = ?", email).
			UpdateColumn("card_key", key)
		if res.Error == nil {
			if res.RowsAffected == 0 {
				return "", common.NewError("client not found:", email)
			}
			return key, nil
		}
		lastErr = res.Error
		if !isCardKeyUniqueViolation(res.Error) {
			return "", res.Error
		}
		key = cardkey.Generate()
	}
	return "", lastErr
}

// isCardKeyUniqueViolation recognises a unique-index collision across the two
// dialects the panel ships (gorm's translated error, SQLite's message,
// PostgreSQL's message). Only ever used to decide whether regenerating the key
// is worth a retry.
func isCardKeyUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unique constraint failed") ||
		strings.Contains(msg, "duplicate key value violates unique constraint")
}

// allocateCardKeyEmails returns count unused email addresses of the shape
// prefix + 4-digit sequence, starting from the lowest number the prefix has
// never used. Numbers already taken by this prefix are skipped, so repeated
// generate calls on one prefix keep producing fresh emails.
func (s *ClientService) allocateCardKeyEmails(prefix string, count int) ([]string, error) {
	if err := validateClientEmail(prefix); err != nil {
		return nil, err
	}
	if err := validateClientEmail(prefix + strings.Repeat("0", cardKeySequenceWidth)); err != nil {
		return nil, err
	}
	taken, err := cardKeyEmailNumbersInUse(prefix)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, count)
	next := 1
	for len(out) < count {
		for {
			if _, used := taken[next]; !used {
				break
			}
			next++
		}
		out = append(out, prefix+fmt.Sprintf("%0*d", cardKeySequenceWidth, next))
		taken[next] = struct{}{}
		next++
	}
	return out, nil
}

// cardKeyEmailNumbersInUse reports which sequence numbers a prefix already
// used, parsed from the clients table in one query.
func cardKeyEmailNumbersInUse(prefix string) (map[int]struct{}, error) {
	var emails []string
	if err := database.GetDB().
		Model(&model.ClientRecord{}).
		Where(`email LIKE ? ESCAPE '\'`, escapeCardKeyLike(prefix)+"%").
		Pluck("email", &emails).Error; err != nil {
		return nil, err
	}
	taken := make(map[int]struct{}, len(emails))
	for _, email := range emails {
		if len(email) <= len(prefix) {
			continue
		}
		// SQLite's LIKE folds ASCII case, so a prefix match may differ in
		// case; only the length matters for slicing the sequence off.
		n, convErr := strconv.Atoi(email[len(prefix):])
		if convErr != nil {
			continue
		}
		taken[n] = struct{}{}
	}
	return taken, nil
}

// escapeCardKeyLike neutralises the LIKE wildcards a user-supplied prefix may
// contain, so a prefix of "%" cannot make the scan enumerate every client.
func escapeCardKeyLike(prefix string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(prefix)
}

// CardKeyExportRow is one line of the card-key CSV export: the display columns
// the operator asked for, already resolved to text.
type CardKeyExportRow struct {
	CardKey    *string
	Email      string
	Comment    string
	TotalGB    int64
	ExpiryTime int64
	Up         int64
	Down       int64
	Enable     bool
	Ports      []int
}

// ExportCardKeyRows returns one row per client, ordered by client id. When
// inboundID is 0 every client is returned; otherwise only the clients attached
// to that inbound. Clients without a card key are included on purpose — the
// operator needs to see which ones are 未生成 — and rows carry their current
// traffic and the ports of the inbounds they are attached to.
func (s *ClientService) ExportCardKeyRows(inboundID int) ([]CardKeyExportRow, error) {
	db := database.GetDB()
	type exportScan struct {
		Id         int
		Email      string
		CardKey    *string
		Comment    string
		TotalGB    int64
		ExpiryTime int64
		Up         int64
		Down       int64
		Enabled    bool
	}
	query := db.Table("clients AS c").
		Select("c.id AS id, c.email AS email, c.card_key AS card_key, c.comment AS comment, " +
			"c.total_gb AS total_gb, c.expiry_time AS expiry_time, c.enable AS enabled, " +
			"COALESCE(ct.up, 0) AS up, COALESCE(ct.down, 0) AS down").
		Joins("LEFT JOIN client_traffics AS ct ON ct.email = c.email")
	if inboundID > 0 {
		query = query.Where("c.id IN (SELECT client_id FROM client_inbounds WHERE inbound_id = ?)", inboundID)
	}
	var scanned []exportScan
	if err := query.Order("c.id ASC").Scan(&scanned).Error; err != nil {
		return nil, err
	}
	if len(scanned) == 0 {
		return []CardKeyExportRow{}, nil
	}

	ids := make([]int, 0, len(scanned))
	for i := range scanned {
		ids = append(ids, scanned[i].Id)
	}
	portsByClient := make(map[int][]int, len(scanned))
	for _, batch := range chunkInts(ids, sqlInChunk) {
		type portScan struct {
			ClientId int
			Port     int
		}
		var links []portScan
		if err := db.Table("client_inbounds AS ci").
			Select("ci.client_id AS client_id, i.port AS port").
			Joins("JOIN inbounds AS i ON i.id = ci.inbound_id").
			Where("ci.client_id IN ?", batch).
			Order("ci.inbound_id ASC").
			Scan(&links).Error; err != nil {
			return nil, err
		}
		for _, l := range links {
			portsByClient[l.ClientId] = appendPortOnce(portsByClient[l.ClientId], l.Port)
		}
	}

	rows := make([]CardKeyExportRow, 0, len(scanned))
	for i := range scanned {
		rows = append(rows, CardKeyExportRow{
			CardKey:    scanned[i].CardKey,
			Email:      scanned[i].Email,
			Comment:    scanned[i].Comment,
			TotalGB:    scanned[i].TotalGB,
			ExpiryTime: scanned[i].ExpiryTime,
			Up:         scanned[i].Up,
			Down:       scanned[i].Down,
			Enable:     scanned[i].Enabled,
			Ports:      portsByClient[scanned[i].Id],
		})
	}
	return rows, nil
}

func appendPortOnce(ports []int, port int) []int {
	for _, p := range ports {
		if p == port {
			return ports
		}
	}
	return append(ports, port)
}

// CardKeyCSVHeader is the export's first line, in the column order the panel
// documents for the 卡密 export.
var CardKeyCSVHeader = []string{"卡密", "邮箱", "备注", "流量GB", "到期时间", "已用上传", "已用下载", "启用", "入站端口"}

// WriteCardKeyCSV renders rows as the export CSV. The BOM is written first so
// Excel on Windows detects UTF-8 and shows the Chinese headers instead of
// mojibake.
func WriteCardKeyCSV(w io.Writer, rows []CardKeyExportRow) error {
	if _, err := io.WriteString(w, "\ufeff"); err != nil {
		return err
	}
	cw := csv.NewWriter(w)
	if err := cw.Write(CardKeyCSVHeader); err != nil {
		return err
	}
	for i := range rows {
		if err := cw.Write(rows[i].CSVRecord()); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

// CSVRecord renders one row in CardKeyCSVHeader order.
func (r CardKeyExportRow) CSVRecord() []string {
	return []string{
		r.CSVCardKey(),
		r.Email,
		r.Comment,
		formatCardKeyGB(r.TotalGB),
		formatCardKeyExpiry(r.ExpiryTime),
		formatCardKeyGB(r.Up),
		formatCardKeyGB(r.Down),
		formatCardKeyBool(r.Enable),
		r.CSVPorts(),
	}
}

// CSVCardKey renders the card key in display form, or the 未生成 marker the
// export uses for a client that has none.
func (r CardKeyExportRow) CSVCardKey() string {
	if r.CardKey == nil || strings.TrimSpace(*r.CardKey) == "" {
		return "未生成"
	}
	return cardkey.Format(*r.CardKey)
}

// CSVPorts renders the inbound ports, comma separated, in ascending order.
func (r CardKeyExportRow) CSVPorts() string {
	if len(r.Ports) == 0 {
		return ""
	}
	sorted := append([]int(nil), r.Ports...)
	sort.Ints(sorted)
	parts := make([]string, 0, len(sorted))
	for _, p := range sorted {
		parts = append(parts, strconv.Itoa(p))
	}
	return strings.Join(parts, ",")
}

// formatCardKeyGB renders a byte count as gigabytes. 0 means "no limit" in this
// panel, and saying so is more useful to an operator than "0.00".
func formatCardKeyGB(bytes int64) string {
	if bytes == 0 {
		return "无限制"
	}
	return strconv.FormatFloat(float64(bytes)/cardKeyGB, 'f', 2, 64)
}

// formatCardKeyExpiry renders an expiry timestamp. The panel stores 0 for
// "never" and a negative value as "that much time after first use", both of
// which are reported in words; everything else is formatted the same way the
// rest of the panel formats dates.
func formatCardKeyExpiry(expiryTime int64) string {
	switch {
	case expiryTime == 0:
		return "永不过期"
	case expiryTime < 0:
		days := float64(-expiryTime) / float64((24*time.Hour)/time.Millisecond)
		if days == float64(int64(days)) {
			return "首次使用后" + strconv.FormatInt(int64(days), 10) + "天"
		}
		return "首次使用后" + strconv.FormatFloat(days, 'f', 2, 64) + "天"
	default:
		return time.UnixMilli(expiryTime).Local().Format("2006-01-02 15:04:05")
	}
}

func formatCardKeyBool(enabled bool) string {
	if enabled {
		return "是"
	}
	return "否"
}
