package mobile

import (
	"strings"
	"testing"
)

type testCB struct{ t *testing.T }

func (c testCB) OnEvent(name, payload string) {
	if len(payload) > 80 {
		payload = payload[:80] + "…"
	}
	c.t.Logf("  [event] %-18s %s", name, payload)
}
func (c testCB) OnLog(level, msg string) { c.t.Logf("  [log/%s] %s", level, msg) }
func (c testCB) SaveFile(name string, data []byte) (string, error) {
	c.t.Logf("  [savefile] %s (%d bytes)", name, len(data))
	return "/saved/" + name, nil
}

func TestFacade(t *testing.T) {
	if err := Start(t.TempDir(), `{"calls":true,"screenShareReceive":true}`, testCB{t}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer Stop()

	if !Started() {
		t.Fatal("Started() == false после Start")
	}
	t.Logf("Features -> %s", FeaturesJSON())

	cases := []struct{ method, args string }{
		{"AppVersion", "[]"},
		{"NetworkReady", "[]"},
		{"GetProfile", "[]"},
		{"Register", `["Тест","tester","био",""]`},
		{"GetProfile", "[]"},
		{"GetChats", "[]"},
		{"GetOnlinePeers", "[]"},
		{"VPNStatus", "[]"},
		{"ListBlocked", "[]"},
		{"GetMessages", `["nobody"]`},
		{"SaveMedia", `["photo.png","data:image/png;base64,aGVsbG8="]`},
	}
	for _, c := range cases {
		out, err := Call(c.method, c.args)
		if err != nil {
			t.Errorf("Call(%s) -> ошибка: %v", c.method, err)
			continue
		}
		if len(out) > 100 {
			out = out[:100] + "…"
		}
		t.Logf("Call(%-16s) -> %s", c.method, out)
	}

	// то, что должно быть отвергнуто
	for _, c := range []struct{ method, args, want string }{
		{"OnStartup", "[]", "not callable"},
		{"NoSuchMethod", "[]", "unknown method"},
		{"Register", `["слишком","мало"]`, "expects 4"},
	} {
		if _, err := Call(c.method, c.args); err == nil {
			t.Errorf("Call(%s) должен был вернуть ошибку", c.method)
		} else if !strings.Contains(err.Error(), c.want) {
			t.Errorf("Call(%s) -> %q, ожидалось про %q", c.method, err, c.want)
		} else {
			t.Logf("отвергнуто верно: %s -> %v", c.method, err)
		}
	}
}

func TestStartRequiresDataDir(t *testing.T) {
	if err := Start("", "{}", testCB{t}); err == nil {
		t.Fatal("Start без dataDir должен падать")
	} else {
		t.Logf("ok: %v", err)
	}
}
