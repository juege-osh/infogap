package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

// Campaign membership is private database state, never a client-side allowlist.
// Awards are registration-only and independent from paid orders/monthly gifts.
const inviteCampaignSchema = `
CREATE TABLE IF NOT EXISTS invite_campaigns(
 invite_id INTEGER PRIMARY KEY REFERENCES invites(id),reward_rank TEXT NOT NULL CHECK(reward_rank='bronze'),
 reward_days INTEGER NOT NULL CHECK(reward_days BETWEEN 1 AND 365),reward_credits INTEGER NOT NULL CHECK(reward_credits BETWEEN 1 AND 100000)
);
CREATE TABLE IF NOT EXISTS invite_campaign_members(
 invite_id INTEGER NOT NULL REFERENCES invite_campaigns(invite_id),email TEXT NOT NULL COLLATE NOCASE,
 PRIMARY KEY(invite_id,email)
);
CREATE TABLE IF NOT EXISTS invite_campaign_awards(
 user_id INTEGER PRIMARY KEY REFERENCES users(id),invite_id INTEGER NOT NULL,email TEXT NOT NULL COLLATE NOCASE,
 tier INTEGER NOT NULL CHECK(tier=1),credits INTEGER NOT NULL CHECK(credits>0),
 starts_at TEXT NOT NULL,ends_at TEXT NOT NULL,ledger_id INTEGER NOT NULL UNIQUE REFERENCES ledger(id),
 UNIQUE(invite_id,email),FOREIGN KEY(invite_id,email) REFERENCES invite_campaign_members(invite_id,email)
);
CREATE INDEX IF NOT EXISTS invite_campaign_awards_time ON invite_campaign_awards(ends_at);
`

type inviteCreateInput struct {
	MaxUses       int      `json:"max_uses"`
	Days          int      `json:"days"`
	Emails        []string `json:"emails"`
	RewardRank    string   `json:"reward_rank"`
	RewardDays    int      `json:"reward_days"`
	RewardCredits int      `json:"reward_credits"`
}

const inviteCreateBodyLimit = 320 * 1024

// Up to 1000 addresses may exceed the ordinary 64 KiB API limit. Only this
// admin endpoint gets the larger bounded decoder; strict JSON rules stay intact.
func decodeInviteCreate(w http.ResponseWriter, r *http.Request, in *inviteCreateInput) bool {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		fail(w, 415, "请使用 JSON 请求")
		return false
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, inviteCreateBodyLimit))
	d.DisallowUnknownFields()
	if err := d.Decode(in); err != nil {
		fail(w, 400, "请求内容不正确或超过活动名单大小限制")
		return false
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		fail(w, 400, "请求内容不正确或超过活动名单大小限制")
		return false
	}
	return true
}

func (in *inviteCreateInput) normalizeCampaign() (bool, error) {
	hasReward := in.RewardRank != "" || in.RewardDays != 0 || in.RewardCredits != 0
	if len(in.Emails) == 0 && !hasReward {
		return false, nil
	}
	if len(in.Emails) == 0 || len(in.Emails) > 1000 {
		return false, errors.New("活动名单需包含 1–1000 个邮箱")
	}
	if in.RewardRank != "bronze" || in.RewardDays < 1 || in.RewardDays > 365 || in.RewardCredits < 1 || in.RewardCredits > 100000 {
		return false, errors.New("活动奖励需为青铜段位，期限 1–365 天，永久积分 1–100000")
	}
	emails := []string{}
	seen := map[string]bool{}
	for _, raw := range in.Emails {
		email := strings.ToLower(strings.TrimSpace(raw))
		if !validEmail(email) {
			return false, errors.New("活动名单含无效邮箱，请检查后重试")
		}
		if !seen[email] {
			emails = append(emails, email)
			seen[email] = true
		}
	}
	sort.Strings(emails)
	in.Emails = emails
	return true, nil
}

func insertInviteCampaign(tx *sql.Tx, inviteID int64, in inviteCreateInput) error {
	if _, err := tx.Exec("INSERT INTO invite_campaigns(invite_id,reward_rank,reward_days,reward_credits) VALUES(?,?,?,?)", inviteID, in.RewardRank, in.RewardDays, in.RewardCredits); err != nil {
		return err
	}
	for _, email := range in.Emails {
		if _, err := tx.Exec("INSERT INTO invite_campaign_members(invite_id,email) VALUES(?,?)", inviteID, email); err != nil {
			return err
		}
	}
	return nil
}

// Only authVerify's new-user branch calls this, after consuming the invitation
// and inserting the user in the same transaction. Existing logins never call it.
func grantInviteCampaign(tx *sql.Tx, inviteID, userID int64, email string, now time.Time) error {
	if inviteID == 0 {
		return nil
	}
	email = strings.ToLower(strings.TrimSpace(email))
	var days, credits int
	err := tx.QueryRow("SELECT c.reward_days,c.reward_credits FROM invite_campaigns c JOIN invite_campaign_members m ON m.invite_id=c.invite_id JOIN users u ON u.id=? AND u.email=m.email WHERE c.invite_id=? AND m.email=?", userID, inviteID, email).Scan(&days, &credits)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	var exists int
	if err = tx.QueryRow("SELECT COUNT(*) FROM invite_campaign_awards WHERE user_id=? OR (invite_id=? AND email=?)", userID, inviteID, email).Scan(&exists); err != nil || exists > 0 {
		return err
	}
	result, err := tx.Exec("INSERT INTO ledger(user_id,kind,amount,created_at,note) VALUES(?,'invite_campaign',?,?,?)", userID, credits, stamp(now), "邀请活动注册奖励 · 一次性永久积分")
	if err != nil {
		return err
	}
	ledgerID, err := result.LastInsertId()
	if err != nil {
		return err
	}
	until := stamp(now.Add(time.Duration(days) * 24 * time.Hour))
	if _, err = tx.Exec("INSERT INTO invite_campaign_awards(user_id,invite_id,email,tier,credits,starts_at,ends_at,ledger_id) VALUES(?,?,?,1,?,?,?,?)", userID, inviteID, email, credits, stamp(now), until, ledgerID); err != nil {
		return err
	}
	// The user is new. The paid_* fields are an effective entitlement cache,
	// recalculated with campaign awards by the normal rank synchronization.
	result, err = tx.Exec("UPDATE users SET credits=credits+?,paid_tier=1,paid_until=? WHERE id=? AND email=?", credits, until, userID, email)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return errors.New("活动奖励账户更新失败")
	}
	return nil
}

func mergeInviteCampaignRank(tx *sql.Tx, userID int64, now time.Time, tier int, until string) (int, string, error) {
	var campaignTier int
	var campaignUntil string
	err := tx.QueryRow("SELECT tier,ends_at FROM invite_campaign_awards WHERE user_id=? AND starts_at<=? AND ends_at>?", userID, stamp(now), stamp(now)).Scan(&campaignTier, &campaignUntil)
	if err == sql.ErrNoRows {
		return tier, until, nil
	}
	if err != nil {
		return tier, until, err
	}
	if campaignTier > tier {
		return campaignTier, campaignUntil, nil
	}
	if campaignTier == tier && campaignUntil > until {
		until = campaignUntil
	}
	return tier, until, nil
}

func (s *server) getInviteCampaign(w http.ResponseWriter, r *http.Request) {
	if s.staff(w, r, true) == nil {
		return
	}
	id := idParam(r)
	var rank string
	var days, credits int
	err := s.db.QueryRow("SELECT reward_rank,reward_days,reward_credits FROM invite_campaigns WHERE invite_id=?", id).Scan(&rank, &days, &credits)
	if err == sql.ErrNoRows {
		fail(w, 404, "此邀请码没有活动奖励")
		return
	}
	if err != nil {
		fail(w, 500, "读取活动失败")
		return
	}
	rows, err := s.db.Query("SELECT m.email,COALESCE(a.starts_at,''),COALESCE(a.ends_at,'') FROM invite_campaign_members m LEFT JOIN invite_campaign_awards a ON a.invite_id=m.invite_id AND a.email=m.email WHERE m.invite_id=? ORDER BY m.email", id)
	if err != nil {
		fail(w, 500, "读取活动失败")
		return
	}
	defer rows.Close()
	emails := []map[string]any{}
	awarded := 0
	for rows.Next() {
		var email, at, until string
		if err = rows.Scan(&email, &at, &until); err != nil {
			fail(w, 500, "读取活动失败")
			return
		}
		if at != "" {
			awarded++
		}
		emails = append(emails, map[string]any{"email": email, "awarded": at != "", "awarded_at": at, "rank_until": until})
	}
	if err = rows.Err(); err != nil {
		fail(w, 500, "读取活动失败")
		return
	}
	send(w, 200, map[string]any{"id": id, "reward_rank": rank, "reward_days": days, "reward_credits": credits, "email_count": len(emails), "awarded_count": awarded, "emails": emails})
}

func campaignAuditNote(in inviteCreateInput) string {
	return fmt.Sprintf("活动邀请：名单 %d 人，青铜 %d 天，一次性永久积分 %d", len(in.Emails), in.RewardDays, in.RewardCredits)
}
