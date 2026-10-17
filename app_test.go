package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	s, e := openStore(filepath.Join(t.TempDir(), "test.db"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.db.Close() })
	return s
}
func execOK(t *testing.T, s *Store, op string, a Args) any {
	t.Helper()
	out, e := s.execute(context.Background(), op, a)
	if e != nil {
		t.Fatalf("%s: %v", op, e)
	}
	return out
}
func snap(t *testing.T, s *Store) Snapshot {
	t.Helper()
	v, e := s.snapshot(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	return v
}
func seedBoard(t *testing.T, s *Store, name string) (Board, []Lane, []Status) {
	t.Helper()
	b := execOK(t, s, "create_board", Args{Name: &name}).(Board)
	v := snap(t, s)
	cols := []Lane{}
	statuses := []Status{}
	for _, c := range v.Columns {
		if c.BoardID == b.ID {
			cols = append(cols, c)
		}
	}
	for _, x := range v.Statuses {
		if x.BoardID == b.ID {
			statuses = append(statuses, x)
		}
	}
	return b, cols, statuses
}

func TestCardsOrderingAndTransactions(t *testing.T) {
	s := testStore(t)
	b, cols, statuses := seedBoard(t, s, "Work")
	other, otherCols, otherStatuses := seedBoard(t, s, "Personal")
	cards := []Card{}
	for _, title := range []string{"A", "B", "C"} {
		cards = append(cards, execOK(t, s, "create_card", Args{BoardID: b.ID, Title: ptr(title)}).(Card))
	}
	moved := execOK(t, s, "move_card", Args{ID: cards[2].ID, ColumnID: cols[0].ID, Position: ptr(0)}).(Card)
	if moved.Position != 0 {
		t.Fatal("incorrect move position")
	}
	ordered := snap(t, s).Cards
	if ordered[0].ID != cards[2].ID || ordered[1].ID != cards[0].ID || ordered[2].ID != cards[1].ID {
		t.Fatal("reorder failed")
	}
	moved = execOK(t, s, "move_card", Args{ID: cards[2].ID, ColumnID: cols[1].ID}).(Card)
	if moved.StatusID != statuses[0].ID {
		t.Fatal("moving changed status")
	}
	if _, e := s.execute(context.Background(), "move_card", Args{ID: cards[2].ID, ColumnID: otherCols[0].ID}); e == nil {
		t.Fatal("cross-board invalid status accepted")
	}
	current := execOK(t, s, "get_card", Args{ID: cards[2].ID}).(map[string]any)["card"].(Card)
	if current.ColumnID != cols[1].ID {
		t.Fatal("failed transfer was not rolled back")
	}
	moved = execOK(t, s, "move_card", Args{ID: cards[2].ID, ColumnID: otherCols[0].ID, StatusID: otherStatuses[0].ID}).(Card)
	if moved.BoardID != other.ID {
		t.Fatal("cross-board transfer failed")
	}
	if _, e := s.execute(context.Background(), "move_card", Args{ID: cards[0].ID, ColumnID: cols[1].ID, Position: ptr(99)}); e == nil {
		t.Fatal("invalid position accepted")
	}
	current = execOK(t, s, "get_card", Args{ID: cards[0].ID}).(map[string]any)["card"].(Card)
	if current.ColumnID != cols[0].ID {
		t.Fatal("failed move changed the card")
	}
	execOK(t, s, "update_card", Args{ID: cards[0].ID, Archived: ptr(true)})
	current = execOK(t, s, "update_card", Args{ID: cards[0].ID, Archived: ptr(false)}).(Card)
	if current.Position != 1 {
		t.Fatal("restore did not append to active order")
	}
	execOK(t, s, "reorder_columns", Args{BoardID: b.ID, IDs: []int64{cols[2].ID, cols[0].ID, cols[1].ID}})
	if snap(t, s).Columns[0].ID != cols[2].ID {
		t.Fatal("column order failed")
	}
	if _, e := s.execute(context.Background(), "reorder_columns", Args{BoardID: b.ID, IDs: []int64{cols[0].ID, cols[0].ID, cols[1].ID}}); e == nil {
		t.Fatal("duplicate order IDs accepted")
	}
}

func TestCardContentLinksAttachmentsAndCascades(t *testing.T) {
	s := testStore(t)
	b, cols, statuses := seedBoard(t, s, "Work")
	c := execOK(t, s, "create_card", Args{BoardID: b.ID, Title: ptr("Task"), Description: ptr("**Markdown**"), Tags: ptr([]string{" work ", "work", "custom"}), Checklist: ptr([]Check{{Text: "Ship", Done: true}}), DueDate: ptr("2026-10-20")}).(Card)
	if len(c.Tags) != 2 || len(c.Checklist) != 1 || !c.Checklist[0].Done {
		t.Fatal("content normalization failed")
	}
	if _, e := s.execute(context.Background(), "update_card", Args{ID: c.ID, DueDate: ptr("2026-02-30")}); e == nil {
		t.Fatal("invalid date accepted")
	}
	second := execOK(t, s, "create_card", Args{BoardID: b.ID, Title: ptr("Second")}).(Card)
	link := execOK(t, s, "create_link", Args{SourceID: c.ID, TargetID: second.ID, TypeID: 2}).(Link)
	if _, e := s.execute(context.Background(), "create_link", Args{SourceID: c.ID, TargetID: c.ID, TypeID: 1}); e == nil {
		t.Fatal("self-link accepted")
	}
	if _, e := s.execute(context.Background(), "delete_column", Args{ID: cols[0].ID}); e == nil {
		t.Fatal("deleted column containing cards")
	}
	execOK(t, s, "delete_column", Args{ID: cols[0].ID, ReplacementID: cols[1].ID})
	execOK(t, s, "delete_status", Args{ID: statuses[0].ID, ReplacementID: statuses[1].ID})
	v := snap(t, s)
	for _, c := range v.Cards {
		if c.ColumnID != cols[1].ID || c.StatusID != statuses[1].ID {
			t.Fatal("replacement failed")
		}
	}
	file := execOK(t, s, "create_attachment", Args{ID: c.ID, Name: ptr("notes.txt"), Data: ptr(base64.StdEncoding.EncodeToString([]byte("hello")))}).(Attachment)
	out := execOK(t, s, "get_attachment", Args{ID: file.ID}).(map[string]any)
	if out["data_base64"] != "aGVsbG8=" {
		t.Fatal("attachment bytes changed")
	}
	links := execOK(t, s, "get_card", Args{ID: second.ID}).(map[string]any)["links"].([]Link)
	if len(links) != 1 || links[0].ID != link.ID {
		t.Fatal("incoming link missing")
	}
	execOK(t, s, "delete_card", Args{ID: c.ID})
	v = snap(t, s)
	if len(v.Links) != 0 || len(v.Attachments) != 0 {
		t.Fatal("card deletion failed to cascade")
	}
	if v.Cards[0].Position != 0 {
		t.Fatal("deleting a card left a gap in positions")
	}
	execOK(t, s, "delete_board", Args{ID: b.ID})
	v = snap(t, s)
	if len(v.Cards)+len(v.Boards)+len(v.Columns)+len(v.Statuses) != 0 {
		t.Fatal("board deletion did not cascade")
	}
}

func TestPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db.sqlite")
	s, e := openStore(path)
	if e != nil {
		t.Fatal(e)
	}
	b, _, _ := seedBoard(t, s, "Persistent")
	c := execOK(t, s, "create_card", Args{BoardID: b.ID, Title: ptr("Keep me")}).(Card)
	execOK(t, s, "delete_relationship_type", Args{ID: 3})
	s.db.Close()
	s, e = openStore(path)
	if e != nil {
		t.Fatal(e)
	}
	defer s.db.Close()
	if snap(t, s).Cards[0].Title != "Keep me" {
		t.Fatal("data did not persist")
	}
	if len(snap(t, s).RelationTypes) != 2 {
		t.Fatal("a deleted relationship type returned after restart")
	}
	execOK(t, s, "delete_card", Args{ID: c.ID})
	next := execOK(t, s, "create_card", Args{BoardID: b.ID, Title: ptr("New")}).(Card)
	if next.ID <= c.ID {
		t.Fatal("card IDs must never be reused")
	}
}

func TestViewsFormsMarkdownAndOrigins(t *testing.T) {
	s := testStore(t)
	b, cols, _ := seedBoard(t, s, "<script>Work</script>")
	c := execOK(t, s, "create_card", Args{BoardID: b.ID, Title: ptr("<script>alert(1)</script>"), Description: ptr("**safe**\n<script>alert(1)</script>\n[bad](javascript:alert(1))"), Checklist: ptr([]Check{{Text: "Check"}})}).(Card)
	app, e := newApp(s)
	if e != nil {
		t.Fatal(e)
	}
	server := httptest.NewServer(app.handler())
	defer server.Close()
	routes := []string{"/", "/board/1", "/board/1?modal=new-card", "/board/1?modal=settings", "/board/1?modal=settings&entity=column&edit=1", "/board/1?modal=settings&entity=status&edit=1", "/board/1?modal=settings&entity=board&edit=1", "/board/1?modal=settings&entity=relationship_type&edit=2", "/?modal=new-board", "/card/1"}
	for _, route := range routes {
		res, e := http.Get(server.URL + route)
		if e != nil {
			t.Fatal(e)
		}
		data, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != 200 {
			t.Fatalf("%s: %d %s", route, res.StatusCode, data)
		}
		if bytes.Contains(data, []byte("<script>alert(1)</script>")) {
			t.Fatalf("unescaped content in %s", route)
		}
	}
	req, _ := http.NewRequest("GET", server.URL+"/card/1", nil)
	req.Header.Set("HX-Request", "true")
	res, e := http.DefaultClient.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	data, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if !bytes.HasPrefix(data, []byte(`<div id="workspace"`)) || bytes.Contains(data, []byte("<!doctype")) {
		t.Fatal("HTMX returned full document")
	}
	form := url.Values{"id": {strconv64(c.ID)}, "title": {"Updated"}, "description": {""}, "tags": {""}, "checklist": {"[x] Done\n[ ] Next"}, "due_date": {""}, "column_id": {strconv64(cols[1].ID)}, "return": {"/card/1"}, "open": {"1"}}
	req, _ = http.NewRequest("POST", server.URL+"/actions/update_card", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	res, e = http.DefaultClient.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	data, _ = io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("form: %s", data)
	}
	updated := snap(t, s).Cards[0]
	if updated.Title != "Updated" || updated.Description != "" || len(updated.Tags) != 0 || len(updated.Checklist) != 2 || !updated.Checklist[0].Done {
		t.Fatal("form update failed")
	}
	req, _ = http.NewRequest("POST", server.URL+"/preview", strings.NewReader(url.Values{"description": {"**safe**\n<script>alert(1)</script>\n[bad](javascript:alert(1))"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, e = http.DefaultClient.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	data, _ = io.ReadAll(res.Body)
	res.Body.Close()
	if !bytes.Contains(data, []byte("<strong>safe</strong>")) || bytes.Contains(data, []byte("<script>")) || bytes.Contains(data, []byte(`href="javascript:`)) {
		t.Fatalf("unsafe preview: %s", data)
	}
	for _, origin := range []string{"https://evil.example", "null"} {
		req, _ = http.NewRequest("POST", server.URL+"/actions/delete_board", strings.NewReader(`{"id":1}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", origin)
		res, e = http.DefaultClient.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		res.Body.Close()
		if res.StatusCode != 403 {
			t.Fatal("foreign origin accepted")
		}
	}
	req, _ = http.NewRequest("GET", server.URL+"/", nil)
	req.Host = "evil.example"
	res, e = http.DefaultClient.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("custom host rejected: %d", res.StatusCode)
	}
	req, _ = http.NewRequest("GET", server.URL+"/", nil)
	req.Host = "kango.localhost"
	res, e = http.DefaultClient.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("Portless host rejected: %d", res.StatusCode)
	}
	if b.ID == 0 {
		t.Fatal("missing board")
	}
}
func strconv64(v int64) string { data, _ := json.Marshal(v); return string(data) }

func TestMCPStreamableHTTP(t *testing.T) {
	s := testStore(t)
	app, e := newApp(s)
	if e != nil {
		t.Fatal(e)
	}
	server := httptest.NewServer(app.handler())
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "generic-client", Version: "1.0"}, nil)
	session, e := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: server.URL + "/mcp", DisableStandaloneSSE: true}, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer session.Close()
	tools, e := session.ListTools(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	if len(tools.Tools) != len(toolSpecs) {
		t.Fatal("tools missing")
	}
	call := func(name string, args any) *mcp.CallToolResult {
		t.Helper()
		r, e := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if e != nil {
			t.Fatal(e)
		}
		return r
	}
	if r := call("create_board", map[string]any{"name": "Via MCP"}); r.IsError {
		t.Fatal(r.Content)
	}
	if r := call("create_card", map[string]any{"board_id": 1, "title": "MCP card", "tags": []string{"agent"}, "checklist": []Check{{Text: "Review", Done: false}}}); r.IsError {
		t.Fatal(r.Content)
	}
	if r := call("update_card", map[string]any{"id": 1, "description": "Updated by a generic MCP client"}); r.IsError {
		t.Fatal(r.Content)
	}
	if r := call("move_card", map[string]any{"id": 1, "column_id": 2, "position": 0}); r.IsError {
		t.Fatal(r.Content)
	}
	if r := call("list_cards", map[string]any{"tag": "agent"}); r.IsError {
		t.Fatal(r.Content)
	}
	if r := call("get_card", map[string]any{"id": 1}); r.IsError || r.StructuredContent == nil {
		t.Fatal("no structured card result")
	}
	if r := call("create_card", map[string]any{"board_id": 1}); !r.IsError {
		t.Fatal("missing required title accepted")
	}
	if r := call("update_card", map[string]any{"id": 1, "unknown": true}); !r.IsError {
		t.Fatal("unknown field accepted")
	}
	if r := call("move_card", map[string]any{"id": 1, "column_id": 999}); !r.IsError {
		t.Fatal("invalid column accepted")
	}
	v := snap(t, s)
	if v.Cards[0].ColumnID != 2 || v.Cards[0].Description != "Updated by a generic MCP client" {
		t.Fatal("MCP state not persisted")
	}
	if r := call("delete_board", map[string]any{"id": 1}); r.IsError {
		t.Fatal(r.Content)
	}
	if len(snap(t, s).Cards) != 0 {
		t.Fatal("MCP delete failed")
	}
}
