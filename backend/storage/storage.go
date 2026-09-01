package storage

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"

	"cloudix/backend/models"
)

type Store struct {
	db   *sql.DB
	path string
}

// DataDir is where the profile/chat database lives. Override the app-name
// suffix with CLOUDIX_INSTANCE to run a second instance side by side.
func DataDir() string {
	dir, _ := os.UserConfigDir()
	appName := "Cloudix"
	if suffix := os.Getenv("CLOUDIX_INSTANCE"); suffix != "" {
		appName = "Cloudix-" + suffix
	}
	appDir := filepath.Join(dir, appName)
	_ = os.MkdirAll(appDir, 0755)
	return appDir
}

func dbPath() string {
	return filepath.Join(DataDir(), "cloudix.db")
}

func Open() (*Store, error) {
	path := dbPath()
	dsn := fmt.Sprintf(
		"file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)",
		path,
	)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}

	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)

	s := &Store{db: db, path: path}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}

	return s, nil
}

func (s *Store) migrate() error {
	if s.db == nil {
		return fmt.Errorf("db not initialized")
	}

	schema := `
CREATE TABLE IF NOT EXISTS profile (
    peer_id TEXT PRIMARY KEY,
    name TEXT,
    username TEXT,
    bio TEXT,
    avatar TEXT,
    created_at INTEGER
);


CREATE TABLE IF NOT EXISTS chats (
    peer_id TEXT PRIMARY KEY,
    name TEXT,
    username TEXT,
    bio TEXT,
    avatar TEXT,
    account_deleted INTEGER DEFAULT 0,
    last_message TEXT DEFAULT '',
    last_timestamp INTEGER DEFAULT 0
);


CREATE TABLE IF NOT EXISTS messages (
    id TEXT PRIMARY KEY,
    chat_id TEXT,
    sender_id TEXT,
    text TEXT,
    media_kind TEXT,
    media_data TEXT,
    ts INTEGER,
    deleted_for_me INTEGER DEFAULT 0,
    deleted_for_both INTEGER DEFAULT 0,
    read INTEGER DEFAULT 0
);


CREATE TABLE IF NOT EXISTS blocklist (
    peer_id TEXT PRIMARY KEY,
    blocked_at INTEGER
);


-- Long-lived X25519 identity for the overlay network. One row.
CREATE TABLE IF NOT EXISTS vpn_identity (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    seed BLOB NOT NULL
);


-- Trust-on-first-use pins for overlay peers' identity keys.
CREATE TABLE IF NOT EXISTS vpn_pins (
    peer_id TEXT PRIMARY KEY,
    pub_key TEXT NOT NULL,
    pinned_at INTEGER
);


CREATE INDEX IF NOT EXISTS idx_messages_chat_ts
    ON messages(chat_id, ts);


CREATE INDEX IF NOT EXISTS idx_messages_chat_sender_read
    ON messages(chat_id, sender_id, read);


CREATE INDEX IF NOT EXISTS idx_messages_id
    ON messages(id);
`
	if _, err := s.db.Exec(schema); err != nil {
		return err
	}

	// Best-effort ALTER TABLE для существующих баз (ошибки "duplicate column"
	// игнорируются, как и для read/deleted_for_me/deleted_for_both выше).
	_, _ = s.db.Exec(`ALTER TABLE messages ADD COLUMN read INTEGER DEFAULT 0`)
	_, _ = s.db.Exec(`ALTER TABLE messages ADD COLUMN deleted_for_me INTEGER DEFAULT 0`)
	_, _ = s.db.Exec(`ALTER TABLE messages ADD COLUMN deleted_for_both INTEGER DEFAULT 0`)
	// NEW: поле для реакций. Пустая строка = нет реакции.
	// reaction      — моя реакция на сообщение
	// reaction_peer — реакция собеседника (раньше была общая колонка, из-за
	//                 чего входящая реакция затирала свою собственную).
	_, _ = s.db.Exec(`ALTER TABLE messages ADD COLUMN reaction TEXT DEFAULT ''`)
	_, _ = s.db.Exec(`ALTER TABLE messages ADD COLUMN reaction_peer TEXT DEFAULT ''`)
	// delivered — доставлено ли ИСХОДЯЩЕЕ сообщение (0 = в очереди на
	// повторную отправку, пока пир не появится в сети). Для входящих и для
	// старых строк значение 1.
	_, _ = s.db.Exec(`ALTER TABLE messages ADD COLUMN delivered INTEGER DEFAULT 1`)

	return nil
}

func (s *Store) SaveProfile(p models.Profile) error {
	if s.db == nil {
		return fmt.Errorf("db not initialized")
	}
	if _, err := s.db.Exec(`DELETE FROM profile`); err != nil {
		return err
	}
	_, err := s.db.Exec(
		`INSERT INTO profile (peer_id,name,username,bio,avatar,created_at) VALUES (?,?,?,?,?,?)`,
		p.PeerID, p.Name, p.Username, p.Bio, p.Avatar, p.CreatedAt,
	)
	return err
}

func (s *Store) LoadProfile() (*models.Profile, error) {
	if s.db == nil {
		return nil, fmt.Errorf("db not initialized")
	}
	row := s.db.QueryRow(`SELECT peer_id,name,username,bio,avatar,created_at FROM profile LIMIT 1`)
	var p models.Profile
	if err := row.Scan(&p.PeerID, &p.Name, &p.Username, &p.Bio, &p.Avatar, &p.CreatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &p, nil
}

func (s *Store) UpsertChatMeta(peerID, name, username, bio, avatar string) error {
	if s.db == nil {
		return fmt.Errorf("db not initialized")
	}
	_, err := s.db.Exec(`
        INSERT INTO chats (peer_id,name,username,bio,avatar,last_message,last_timestamp,account_deleted)
        VALUES (?,?,?,?,?,'',0,0)
        ON CONFLICT(peer_id) DO UPDATE SET
            name=excluded.name,
            username=excluded.username,
            bio=excluded.bio,
            avatar=excluded.avatar
    `, peerID, name, username, bio, avatar)
	return err
}

// UpsertChatMetaIfMissing creates a placeholder chat so an incoming message has
// a row to attach to, without overwriting a name already known.
func (s *Store) UpsertChatMetaIfMissing(peerID string) error {
	if s.db == nil {
		return fmt.Errorf("db not initialized")
	}
	_, err := s.db.Exec(`
        INSERT INTO chats (peer_id,name,username,bio,avatar,last_message,last_timestamp,account_deleted)
        VALUES (?,?,'','','','',0,0)
        ON CONFLICT(peer_id) DO NOTHING
    `, peerID, peerID)
	return err
}

func (s *Store) UpsertChatMetaIfExists(peerID, name, username, bio, avatar string) error {
	if s.db == nil {
		return fmt.Errorf("db not initialized")
	}
	_, err := s.db.Exec(`
        UPDATE chats
        SET name=?, username=?, bio=?, avatar=?
        WHERE peer_id=?
    `, name, username, bio, avatar, peerID)
	return err
}

func (s *Store) TouchChatLastMessage(peerID, preview string, ts int64) error {
	if s.db == nil {
		return fmt.Errorf("db not initialized")
	}
	_, err := s.db.Exec(`
        UPDATE chats
        SET last_message=?, last_timestamp=?
        WHERE peer_id=?
    `, preview, ts, peerID)
	return err
}

func (s *Store) MarkAccountDeleted(peerID string) error {
	if s.db == nil {
		return fmt.Errorf("db not initialized")
	}
	_, err := s.db.Exec(`UPDATE chats SET account_deleted=1 WHERE peer_id=?`, peerID)
	return err
}

func (s *Store) DeleteChat(peerID string) error {
	if s.db == nil {
		return fmt.Errorf("db not initialized")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM chats WHERE peer_id=?`, peerID); err != nil {
		_ = tx.Rollback()
		return err
	}
	if _, err := tx.Exec(`DELETE FROM messages WHERE chat_id=?`, peerID); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func (s *Store) ListChats(myPeerID string) ([]models.Chat, error) {
	if s.db == nil {
		return nil, fmt.Errorf("db not initialized")
	}
	rows, err := s.db.Query(`
        SELECT
            c.peer_id,
            c.name,
            c.username,
            c.bio,
            c.avatar,
            c.account_deleted,
            COALESCE(c.last_message, ''),
            COALESCE(c.last_timestamp, 0),
            (
                SELECT COUNT(*)
                FROM messages m
                WHERE m.chat_id = c.peer_id
                  AND m.sender_id != ?
                  AND m.read = 0
                  AND m.deleted_for_me = 0
                  AND m.deleted_for_both = 0
            )
        FROM chats c
        ORDER BY c.last_timestamp DESC
    `, myPeerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []models.Chat
	for rows.Next() {
		var c models.Chat
		var deleted int
		if err := rows.Scan(
			&c.PeerID,
			&c.Name,
			&c.Username,
			&c.Bio,
			&c.Avatar,
			&deleted,
			&c.LastMessage,
			&c.LastTimestamp,
			&c.Unread,
		); err != nil {
			return nil, err
		}
		c.AccountDeleted = deleted == 1
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) InsertMessage(m models.Message) error {
	if s.db == nil {
		return fmt.Errorf("db not initialized")
	}
	_, err := s.db.Exec(`
        INSERT INTO messages (
            id, chat_id, sender_id, text, media_kind, media_data, ts,
            deleted_for_me, deleted_for_both, read, reaction, reaction_peer, delivered
        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    `,
		m.ID,
		m.ChatID,
		m.SenderID,
		m.Text,
		m.MediaKind,
		m.MediaData,
		m.Timestamp,
		boolToInt(m.DeletedForMe),
		boolToInt(m.DeletedForBoth),
		boolToInt(m.Read),
		m.Reaction,
		m.ReactionPeer,
		boolToInt(m.Delivered),
	)
	return err
}

func (s *Store) ListMessages(chatID string) ([]models.Message, error) {
	if s.db == nil {
		return nil, fmt.Errorf("db not initialized")
	}
	rows, err := s.db.Query(`
        SELECT
            id,
            chat_id,
            sender_id,
            COALESCE(text, ''),
            COALESCE(media_kind, ''),
            COALESCE(media_data, ''),
            ts,
            COALESCE(deleted_for_me, 0),
            COALESCE(deleted_for_both, 0),
            COALESCE(read, 0),
            COALESCE(reaction, ''),
            COALESCE(reaction_peer, ''),
            COALESCE(delivered, 1)
        FROM messages
        WHERE chat_id=?
          AND deleted_for_me=0
        ORDER BY ts ASC
    `, chatID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []models.Message
	for rows.Next() {
		var m models.Message
		var deletedMe, deletedBoth, read, delivered int
		if err := rows.Scan(
			&m.ID,
			&m.ChatID,
			&m.SenderID,
			&m.Text,
			&m.MediaKind,
			&m.MediaData,
			&m.Timestamp,
			&deletedMe,
			&deletedBoth,
			&read,
			&m.Reaction,
			&m.ReactionPeer,
			&delivered,
		); err != nil {
			return nil, err
		}
		m.DeletedForMe = deletedMe == 1
		m.DeletedForBoth = deletedBoth == 1
		m.Read = read == 1
		m.Delivered = delivered == 1
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) SoftDeleteMessage(id string, forBoth bool) error {
	if s.db == nil {
		return fmt.Errorf("db not initialized")
	}
	if forBoth {
		_, err := s.db.Exec(`
            UPDATE messages
            SET deleted_for_me=1, deleted_for_both=1
            WHERE id=?
        `, id)
		return err
	}
	_, err := s.db.Exec(`
        UPDATE messages
        SET deleted_for_me=1
        WHERE id=?
    `, id)
	return err
}

// SetMessageReaction устанавливает (или снимает, если emoji == "") реакцию на
// сообщение по его ID. mine=true — моя реакция, mine=false — реакция
// собеседника. Раньше была одна общая колонка, и входящая реакция затирала
// собственную.
func (s *Store) SetMessageReaction(messageID, emoji string, mine bool) error {
	if s.db == nil {
		return fmt.Errorf("db not initialized")
	}
	if messageID == "" {
		return fmt.Errorf("messageID is required")
	}
	col := "reaction_peer"
	if mine {
		col = "reaction"
	}
	_, err := s.db.Exec(`UPDATE messages SET `+col+`=? WHERE id=?`, emoji, messageID)
	return err
}

// MarkMessageDelivered помечает исходящее сообщение как доставленное (успешно
// отправленное по транспорту).
func (s *Store) MarkMessageDelivered(id string) error {
	if s.db == nil {
		return fmt.Errorf("db not initialized")
	}
	_, err := s.db.Exec(`UPDATE messages SET delivered=1 WHERE id=?`, id)
	return err
}

// ListAllUndelivered возвращает все мои исходящие сообщения, которые ещё не
// доставлены (пир был не в сети в момент отправки). Порядок — по времени.
func (s *Store) ListAllUndelivered(myPeerID string) ([]models.Message, error) {
	if s.db == nil {
		return nil, fmt.Errorf("db not initialized")
	}
	rows, err := s.db.Query(`
        SELECT id, chat_id, sender_id,
               COALESCE(text,''), COALESCE(media_kind,''), COALESCE(media_data,''), ts
        FROM messages
        WHERE sender_id=?
          AND delivered=0
          AND deleted_for_both=0
        ORDER BY ts ASC
    `, myPeerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []models.Message
	for rows.Next() {
		var m models.Message
		if err := rows.Scan(&m.ID, &m.ChatID, &m.SenderID, &m.Text, &m.MediaKind, &m.MediaData, &m.Timestamp); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) MarkMessagesRead(chatID, myPeerID string) ([]string, error) {
	if s.db == nil {
		return nil, fmt.Errorf("db not initialized")
	}
	rows, err := s.db.Query(`
        SELECT id
        FROM messages
        WHERE chat_id=?
          AND sender_id!=?
          AND read=0
          AND deleted_for_me=0
          AND deleted_for_both=0
    `, chatID, myPeerID)
	if err != nil {
		return nil, err
	}

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	_ = rows.Close()

	if len(ids) == 0 {
		return nil, nil
	}

	_, err = s.db.Exec(`
        UPDATE messages
        SET read=1
        WHERE chat_id=?
          AND sender_id!=?
          AND read=0
          AND deleted_for_me=0
          AND deleted_for_both=0
    `, chatID, myPeerID)
	if err != nil {
		return nil, err
	}

	return ids, nil
}

func (s *Store) MarkMessagesReadByIDs(ids []string) error {
	if s.db == nil {
		return fmt.Errorf("db not initialized")
	}
	if len(ids) == 0 {
		return nil
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}

	stmt, err := tx.Prepare(`UPDATE messages SET read=1 WHERE id=?`)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	defer stmt.Close()

	for _, id := range ids {
		if _, err := stmt.Exec(id); err != nil {
			_ = tx.Rollback()
			return err
		}
	}

	return tx.Commit()
}

// LoadVPNIdentity returns the stored overlay identity seed, or nil if none has
// been generated yet.
func (s *Store) LoadVPNIdentity() ([]byte, error) {
	if s.db == nil {
		return nil, fmt.Errorf("db not initialized")
	}
	row := s.db.QueryRow(`SELECT seed FROM vpn_identity WHERE id=1`)
	var seed []byte
	if err := row.Scan(&seed); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return seed, nil
}

func (s *Store) SaveVPNIdentity(seed []byte) error {
	if s.db == nil {
		return fmt.Errorf("db not initialized")
	}
	_, err := s.db.Exec(
		`INSERT INTO vpn_identity (id, seed) VALUES (1, ?)
         ON CONFLICT(id) DO UPDATE SET seed=excluded.seed`,
		seed,
	)
	return err
}

// PinnedKey returns the identity key previously seen for a peer. A mismatch on
// a later sighting means someone is trying to impersonate them.
func (s *Store) PinnedKey(peerID string) (string, bool) {
	if s.db == nil {
		return "", false
	}
	row := s.db.QueryRow(`SELECT pub_key FROM vpn_pins WHERE peer_id=?`, peerID)
	var key string
	if err := row.Scan(&key); err != nil {
		return "", false
	}
	return key, true
}

func (s *Store) PinKey(peerID, pubKey string) error {
	if s.db == nil {
		return fmt.Errorf("db not initialized")
	}
	_, err := s.db.Exec(
		`INSERT INTO vpn_pins (peer_id, pub_key, pinned_at) VALUES (?, ?, ?)
         ON CONFLICT(peer_id) DO NOTHING`,
		peerID, pubKey, time.Now().Unix(),
	)
	return err
}

func (s *Store) BlockPeer(peerID string) error {
	if s.db == nil {
		return fmt.Errorf("db not initialized")
	}
	_, err := s.db.Exec(`
        INSERT OR REPLACE INTO blocklist (peer_id, blocked_at)
        VALUES (?, ?)
    `, peerID, time.Now().Unix())
	return err
}

func (s *Store) UnblockPeer(peerID string) error {
	if s.db == nil {
		return fmt.Errorf("db not initialized")
	}
	_, err := s.db.Exec(`DELETE FROM blocklist WHERE peer_id=?`, peerID)
	return err
}

func (s *Store) IsBlocked(peerID string) bool {
	if s.db == nil {
		return false
	}
	row := s.db.QueryRow(`SELECT 1 FROM blocklist WHERE peer_id=?`, peerID)
	var x int
	return row.Scan(&x) == nil
}

func (s *Store) ListBlocked() ([]string, error) {
	if s.db == nil {
		return nil, fmt.Errorf("db not initialized")
	}
	rows, err := s.db.Query(`SELECT peer_id FROM blocklist`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	err := s.db.Close()
	s.db = nil
	return err
}

func (s *Store) WipeAll() error {
	if s == nil {
		return nil
	}
	if s.db != nil {
		_ = s.db.Close()
		s.db = nil
	}
	if s.path == "" {
		return fmt.Errorf("db path is empty")
	}
	if err := os.Remove(s.path); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.Remove(s.path + "-wal"); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.Remove(s.path + "-shm"); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
