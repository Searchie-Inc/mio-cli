package cmd

import (
	"net/http"
	"testing"

	"github.com/Searchie-Inc/mio-cli/internal/errs"
)

func TestContentReorder_Body(t *testing.T) {
	srv, method, path, _, body := captureAdminReq(t, http.StatusNoContent, "")
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123",
			"content", "reorder", "--order", "cnt_1,cnt_2,cnt_3")...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit=%d want ExitOK; stderr=%q", res.Code, res.Stderr)
	}
	if *method != http.MethodPost {
		t.Errorf("method=%q want POST", *method)
	}
	if want := "/api/v1/teams/t_team1/hubs/hub_123/content/reorder"; *path != want {
		t.Errorf("path=%q want %q", *path, want)
	}

	typ, attrs := decodeDataTypeAttrs(t, *body)
	if typ != "content_nodes" {
		t.Errorf("data.type=%q want content_nodes", typ)
	}

	rawItems, ok := attrs["items"]
	if !ok {
		t.Fatalf("attributes.items missing; attrs=%v", attrs)
	}
	items, ok := rawItems.([]any)
	if !ok {
		t.Fatalf("attributes.items is not an array; got %T (%v)", rawItems, rawItems)
	}
	wantIDs := []string{"cnt_1", "cnt_2", "cnt_3"}
	if len(items) != len(wantIDs) {
		t.Fatalf("items len=%d want %d; items=%v", len(items), len(wantIDs), items)
	}
	for i, it := range items {
		m, ok := it.(map[string]any)
		if !ok {
			t.Fatalf("items[%d] is not an object; got %T (%v)", i, it, it)
		}
		if m["id"] != wantIDs[i] {
			t.Errorf("items[%d].id=%v want %q", i, m["id"], wantIDs[i])
		}
		if m["position"] != float64(i) {
			t.Errorf("items[%d].position=%v want %d", i, m["position"], i)
		}
	}

	if _, ok := attrs["order"]; ok {
		t.Errorf("attributes.order must not be sent (backend forbids it); attrs=%v", attrs)
	}
	if _, ok := attrs["parent_id"]; ok {
		t.Errorf("attributes.parent_id must not be sent (backend forbids it); attrs=%v", attrs)
	}
}

func TestContentReorder_TrimsAndDropsEmpties(t *testing.T) {
	srv, _, _, _, body := captureAdminReq(t, http.StatusNoContent, "")
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123",
			"content", "reorder", "--order", " cnt_1 , ,cnt_2,")...)

	if res.Code != errs.ExitOK {
		t.Fatalf("exit=%d want ExitOK; stderr=%q", res.Code, res.Stderr)
	}
	_, attrs := decodeDataTypeAttrs(t, *body)
	items, ok := attrs["items"].([]any)
	if !ok {
		t.Fatalf("attributes.items is not an array; attrs=%v", attrs)
	}
	if len(items) != 2 {
		t.Fatalf("items len=%d want 2 (empties dropped); items=%v", len(items), items)
	}
	want := []string{"cnt_1", "cnt_2"}
	for i, it := range items {
		m := it.(map[string]any)
		if m["id"] != want[i] {
			t.Errorf("items[%d].id=%v want %q", i, m["id"], want[i])
		}
		if m["position"] != float64(i) {
			t.Errorf("items[%d].position=%v want %d", i, m["position"], i)
		}
	}
}

func TestContentReorder_RequiresOrder(t *testing.T) {
	srv, fired := firedGuardServer(t)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123", "content", "reorder")...)
	if res.Code != errs.ExitUsage {
		t.Errorf("exit=%d want ExitUsage; stderr=%q", res.Code, res.Stderr)
	}
	if *fired {
		t.Error("missing --order must exit before any HTTP request")
	}
}

func TestContentReorder_RejectsEmptyOrder(t *testing.T) {
	srv, fired := firedGuardServer(t)
	res := runContract(t, baseEnv(srv.URL),
		withTeam("t_team1", "--hub", "hub_123", "content", "reorder", "--order", " , , ")...)
	if res.Code != errs.ExitUsage {
		t.Errorf("exit=%d want ExitUsage; stderr=%q", res.Code, res.Stderr)
	}
	if *fired {
		t.Error("an all-empty --order must exit before any HTTP request")
	}
}
