package store

import (
	"time"

	"github.com/lib/pq"
)

// Field names follow the SQL in migrations/; secrets never leave as JSON.

type Proxy struct {
	ID            int64      `json:"id"`
	Kind          string     `json:"kind"`
	Host          *string    `json:"host"`
	Port          *int       `json:"port"`
	Username      *string    `json:"username"`
	PasswordEnc   []byte     `json:"-"`
	Status        string     `json:"status"`
	MaxAccounts   *int       `json:"max_accounts"`
	FailCount     int        `json:"fail_count"`
	CooldownUntil *time.Time `json:"cooldown_until"`
	CooldownCount int        `json:"cooldown_count"`
	LastOkAt      *time.Time `json:"last_ok_at"`
	LastError     *string    `json:"last_error"`
	LastErrorAt   *time.Time `json:"last_error_at"`
	CreatedAt     time.Time  `json:"created_at"`
}

func (Proxy) TableName() string { return "proxies" }

type Account struct {
	ID              int64      `json:"id"`
	Username        string     `json:"username"`
	PasswordEnc     []byte     `json:"-"`
	TokenEnc        []byte     `json:"-"`
	SharedSecretEnc []byte     `json:"-"`
	GuardType       *string    `json:"guard_type"`
	ProxyID         *int64     `json:"proxy_id"`
	PinnedProxy     bool       `json:"pinned_proxy"`
	SteamID         *int64     `json:"steam_id"`
	OthersHistory   *bool      `json:"others_history"`
	Status          string     `json:"status"`
	GCDailyLimit    int        `gorm:"column:gc_daily_limit" json:"gc_daily_limit"`
	GCRemaining     int        `gorm:"column:gc_remaining" json:"gc_remaining"`
	GCResetAt       time.Time  `gorm:"column:gc_reset_at" json:"gc_reset_at"`
	CooldownUntil   *time.Time `json:"cooldown_until"`
	LastLoginAt     *time.Time `json:"last_login_at"`
	LastError       *string    `json:"last_error"`
	LastErrorAt     *time.Time `json:"last_error_at"`
	CreatedAt       time.Time  `json:"created_at"`
}

func (Account) TableName() string { return "steam_accounts" }

type Player struct {
	AccountID      int64      `gorm:"primaryKey;autoIncrement:false" json:"account_id"`
	HistoryPrivate bool       `json:"history_private"`
	LastMatchID    *int64     `json:"last_match_id"`
	CheckedAt      *time.Time `json:"checked_at"`
	DiscoveredAt   time.Time  `gorm:"default:now()" json:"discovered_at"`
}

func (Player) TableName() string { return "players" }

type Match struct {
	MatchID      int64      `gorm:"primaryKey;autoIncrement:false" json:"match_id"`
	Source       string     `json:"source"`
	DetailsAt    *time.Time `json:"details_at"`
	Cluster      *int       `json:"cluster"`
	ReplaySalt   *int64     `json:"replay_salt"`
	ReplayState  *string    `json:"replay_state"`
	StartTime    *time.Time `json:"start_time"`
	DurationS    *int       `json:"duration_s"`
	GameMode     *int       `json:"game_mode"`
	LobbyType    *int       `json:"lobby_type"`
	RadiantWin   *bool      `json:"radiant_win"`
	RadiantScore *int       `json:"radiant_score"`
	DireScore    *int       `json:"dire_score"`
	AverageRank  *int       `json:"average_rank"`
	RawDetails   []byte     `json:"-"`
	DiscoveredAt time.Time  `gorm:"default:now()" json:"discovered_at"`
}

func (Match) TableName() string { return "matches" }

type MatchPlayer struct {
	MatchID     int64         `gorm:"primaryKey;autoIncrement:false" json:"match_id"`
	PlayerSlot  int           `gorm:"primaryKey;autoIncrement:false" json:"player_slot"`
	AccountID   *int64        `json:"account_id"`
	HeroID      int           `json:"hero_id"`
	Kills       *int          `json:"kills"`
	Deaths      *int          `json:"deaths"`
	Assists     *int          `json:"assists"`
	LastHits    *int          `json:"last_hits"`
	Denies      *int          `json:"denies"`
	GoldPerMin  *int          `json:"gold_per_min"`
	XpPerMin    *int          `json:"xp_per_min"`
	NetWorth    *int          `json:"net_worth"`
	HeroDamage  *int          `json:"hero_damage"`
	TowerDamage *int          `json:"tower_damage"`
	HeroHealing *int          `json:"hero_healing"`
	Level       *int          `json:"level"`
	Items       pq.Int64Array `gorm:"type:bigint[]" json:"items"`
}

func (MatchPlayer) TableName() string { return "match_players" }

// ReplayMeta is a replay row without its data, for lists and metadata.
type ReplayMeta struct {
	MatchID      int64     `json:"match_id"`
	URL          string    `json:"url"`
	Compression  string    `json:"compression"`
	SizeBytes    int64     `json:"size_bytes"`
	Sha256       []byte    `json:"sha256"`
	DownloadedAt time.Time `json:"downloaded_at"`
}

func (ReplayMeta) TableName() string { return "replays" }

type Replay struct {
	ReplayMeta
	Data []byte `json:"-"`
}

func (Replay) TableName() string { return "replays" }

type Task struct {
	ID          int64      `json:"id"`
	Kind        string     `json:"kind"`
	TargetID    *int64     `json:"target_id"`
	Status      string     `json:"status"`
	Priority    int        `json:"priority"`
	RunAfter    time.Time  `json:"run_after"`
	Attempts    int        `json:"attempts"`
	MaxAttempts int        `json:"max_attempts"`
	AccountID   *int64     `json:"account_id"`
	ProxyID     *int64     `json:"proxy_id"`
	ParentID    *int64     `json:"parent_id"`
	LastError   *string    `json:"last_error"`
	CreatedBy   string     `json:"created_by"`
	CreatedAt   time.Time  `json:"created_at"`
	StartedAt   *time.Time `json:"started_at"`
	FinishedAt  *time.Time `json:"finished_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

func (Task) TableName() string { return "tasks" }

type GCRequest struct {
	ID          int64     `json:"id"`
	AccountID   int64     `json:"account_id"`
	ProxyID     *int64    `json:"proxy_id"`
	TaskID      *int64    `json:"task_id"`
	Kind        string    `json:"kind"`
	TargetID    *int64    `json:"target_id"`
	Result      string    `json:"result"`
	EResult     *int      `gorm:"column:eresult" json:"eresult"`
	LatencyMs   int       `json:"latency_ms"`
	RequestedAt time.Time `gorm:"default:now()" json:"requested_at"`
}

func (GCRequest) TableName() string { return "gc_requests" }

// Task kinds and statuses.
const (
	KindWebAPISequence = "webapi_sequence"
	KindTopLiveGames   = "top_live_games"
	KindPlayerHistory  = "player_history"
	KindMatchDetails   = "match_details"
	KindReplayDownload = "replay_download"
	KindProxyCheck     = "proxy_check"

	StatusQueued    = "queued"
	StatusRunning   = "running"
	StatusDone      = "done"
	StatusSkipped   = "skipped"
	StatusFailed    = "failed"
	StatusCancelled = "cancelled"
)

// Kinds lists task kinds the API may create; the others belong to the old discovery scope.
var Kinds = []string{KindMatchDetails, KindReplayDownload}
