package storage

import (
	"database/sql"
	"testing"

	"cloudix/backend/models"
)

// openTemp points the store at a per-test directory so these never touch the
// real profile in ~/Library/Application Support.
func openTemp(t *testing.T) *Store {
	t.Helper()
	SetDataDir(t.TempDir())
	t.Cleanup(func() { SetDataDir("") })
	s, err := Open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// The profile decoration is chosen by its owner and shown to everyone, so it has
// to survive a round trip rather than being dropped on save like a column that
// only exists in the struct.
func TestProfileDecorRoundTrip(t *testing.T) {
	s := openTemp(t)

	want := models.Profile{
		PeerID: "P-1234", Name: "Тест", Username: "@tester",
		Bio: "био", Background: "teal-gradient", Pattern: "waves",
		CreatedAt: 1700000000,
	}
	if err := s.SaveProfile(want); err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}

	got, err := s.LoadProfile()
	if err != nil {
		t.Fatalf("LoadProfile: %v", err)
	}
	if got == nil {
		t.Fatal("профиль не сохранился")
	}
	if got.Background != want.Background || got.Pattern != want.Pattern {
		t.Errorf("оформление потерялось: background=%q pattern=%q", got.Background, got.Pattern)
	}
}

// A peer's decoration arrives with their profile_update and has to land on the
// chat row, otherwise their card looks undecorated to us alone.
func TestChatMetaDecorRoundTrip(t *testing.T) {
	s := openTemp(t)

	meta := models.ChatMeta{
		PeerID: "P-peer", Name: "Пир", Username: "@peer",
		Background: "purple", Pattern: "stars",
	}
	if err := s.UpsertChatMeta(meta); err != nil {
		t.Fatalf("UpsertChatMeta: %v", err)
	}

	chats, err := s.ListChats("P-me")
	if err != nil {
		t.Fatalf("ListChats: %v", err)
	}
	if len(chats) != 1 {
		t.Fatalf("ожидался один чат, получено %d", len(chats))
	}
	if chats[0].Background != "purple" || chats[0].Pattern != "stars" {
		t.Errorf("оформление чата потерялось: %+v", chats[0])
	}
}

func TestCallLog(t *testing.T) {
	s := openTemp(t)

	entries := []models.CallEntry{
		{ID: "c1", PeerID: "P-a", Name: "A", Direction: models.CallOutgoing,
			Outcome: models.CallAccepted, Video: true, Duration: 42, Timestamp: 1000},
		{ID: "c2", PeerID: "P-b", Name: "B", Direction: models.CallIncoming,
			Outcome: models.CallMissed, Timestamp: 2000},
	}
	for _, e := range entries {
		if err := s.InsertCall(e); err != nil {
			t.Fatalf("InsertCall: %v", err)
		}
	}

	got, err := s.ListCalls(0)
	if err != nil {
		t.Fatalf("ListCalls: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("ожидалось 2 записи, получено %d", len(got))
	}
	// Newest first, so the log reads like a call history and not a queue.
	if got[0].ID != "c2" {
		t.Errorf("порядок не по убыванию времени: %q первым", got[0].ID)
	}
	if !got[1].Video || got[1].Duration != 42 {
		t.Errorf("поля звонка потерялись: %+v", got[1])
	}

	// Re-inserting the same id must update it, not duplicate: the frontend
	// writes the row once when the call ends, but a retry must stay harmless.
	updated := entries[0]
	updated.Outcome = models.CallDeclined
	if err := s.InsertCall(updated); err != nil {
		t.Fatalf("InsertCall (повтор): %v", err)
	}
	got, _ = s.ListCalls(0)
	if len(got) != 2 {
		t.Fatalf("повторная вставка задвоила запись: %d", len(got))
	}

	if err := s.ClearCalls(); err != nil {
		t.Fatalf("ClearCalls: %v", err)
	}
	got, _ = s.ListCalls(0)
	if len(got) != 0 {
		t.Errorf("после очистки осталось %d записей", len(got))
	}
}

// "Delete the local profile" must actually leave nothing behind — including the
// call log, which is the newest thing to forget about.
func TestWipeAllLeavesNothing(t *testing.T) {
	dir := t.TempDir()
	SetDataDir(dir)
	t.Cleanup(func() { SetDataDir("") })

	s, err := Open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := s.SaveProfile(models.Profile{PeerID: "P-me", Name: "Я"}); err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}
	if err := s.UpsertChatMeta(models.ChatMeta{PeerID: "P-peer", Name: "Пир"}); err != nil {
		t.Fatalf("UpsertChatMeta: %v", err)
	}
	if err := s.InsertCall(models.CallEntry{
		ID: "c1", PeerID: "P-peer", Direction: models.CallIncoming,
		Outcome: models.CallAccepted, Timestamp: 1,
	}); err != nil {
		t.Fatalf("InsertCall: %v", err)
	}

	if err := s.WipeAll(); err != nil {
		t.Fatalf("WipeAll: %v", err)
	}

	// Logout reopens the store straight after wiping, which is what the user
	// then sees — so that is what the test inspects.
	fresh, err := Open()
	if err != nil {
		t.Fatalf("Open после WipeAll: %v", err)
	}
	defer fresh.Close()

	profile, err := fresh.LoadProfile()
	if err != nil {
		t.Fatalf("LoadProfile: %v", err)
	}
	if profile != nil {
		t.Errorf("профиль пережил WipeAll: %+v", profile)
	}
	chats, err := fresh.ListChats("P-me")
	if err != nil {
		t.Fatalf("ListChats: %v", err)
	}
	if len(chats) != 0 {
		t.Errorf("чаты пережили WipeAll: %d", len(chats))
	}
	calls, err := fresh.ListCalls(0)
	if err != nil {
		t.Fatalf("ListCalls: %v", err)
	}
	if len(calls) != 0 {
		t.Errorf("журнал звонков пережил WipeAll: %d", len(calls))
	}
}

// Someone upgrading has a database from before these columns existed. The
// migration is best-effort ALTER TABLE, so this checks an old file still opens
// and reads back rather than failing on the first SELECT of a missing column.
func TestMigrationFromOldSchema(t *testing.T) {
	dir := t.TempDir()
	SetDataDir(dir)
	t.Cleanup(func() { SetDataDir("") })

	path := dbPath()

	// The schema as it was before profile decoration and the call log.
	old, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	_, err = old.Exec(`
        CREATE TABLE profile (
            peer_id TEXT PRIMARY KEY, name TEXT, username TEXT,
            bio TEXT, avatar TEXT, created_at INTEGER
        );
        CREATE TABLE chats (
            peer_id TEXT PRIMARY KEY, name TEXT, username TEXT, bio TEXT,
            avatar TEXT, account_deleted INTEGER DEFAULT 0,
            last_message TEXT DEFAULT '', last_timestamp INTEGER DEFAULT 0
        );
        INSERT INTO profile VALUES ('P-old','Старый','@old','био','',123);
        INSERT INTO chats (peer_id,name,username,bio,avatar) VALUES ('P-peer','Пир','@peer','','');
    `)
	if err != nil {
		t.Fatalf("создание старой схемы: %v", err)
	}
	if err := old.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	s, err := Open()
	if err != nil {
		t.Fatalf("Open старой базы: %v", err)
	}
	defer s.Close()

	profile, err := s.LoadProfile()
	if err != nil {
		t.Fatalf("LoadProfile: %v", err)
	}
	if profile == nil || profile.Name != "Старый" {
		t.Fatalf("старый профиль не прочитался: %+v", profile)
	}
	if profile.Background != "" || profile.Pattern != "" {
		t.Errorf("новые поля должны быть пустыми, получено %q/%q",
			profile.Background, profile.Pattern)
	}

	chats, err := s.ListChats("P-old")
	if err != nil {
		t.Fatalf("ListChats: %v", err)
	}
	if len(chats) != 1 || chats[0].Name != "Пир" {
		t.Fatalf("старый чат не прочитался: %+v", chats)
	}

	// The call log table did not exist at all in the old file.
	if _, err := s.ListCalls(0); err != nil {
		t.Errorf("журнал звонков не создался при миграции: %v", err)
	}
}
