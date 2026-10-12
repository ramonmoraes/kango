package main

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite"
)

type Board struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}
type Lane struct {
	ID       int64  `json:"id"`
	BoardID  int64  `json:"board_id"`
	Name     string `json:"name"`
	Position int    `json:"position"`
}
type Status struct {
	ID      int64  `json:"id"`
	BoardID int64  `json:"board_id"`
	Name    string `json:"name"`
}
type RelationType struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Inverse string `json:"inverse"`
}
type Check struct {
	Text string `json:"text"`
	Done bool   `json:"done"`
}
type Card struct {
	ID          int64    `json:"id"`
	BoardID     int64    `json:"board_id"`
	ColumnID    int64    `json:"column_id"`
	StatusID    int64    `json:"status_id"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Tags        []string `json:"tags"`
	Checklist   []Check  `json:"checklist"`
	DueDate     string   `json:"due_date"`
	Archived    bool     `json:"archived"`
	Position    int      `json:"position"`
	UpdatedAt   string   `json:"updated_at"`
}
type Link struct {
	ID       int64 `json:"id"`
	SourceID int64 `json:"source_id"`
	TargetID int64 `json:"target_id"`
	TypeID   int64 `json:"type_id"`
}
type Attachment struct {
	ID     int64  `json:"id"`
	CardID int64  `json:"card_id"`
	Name   string `json:"name"`
	Size   int    `json:"size"`
}
type Snapshot struct {
	Boards        []Board        `json:"boards"`
	Columns       []Lane         `json:"columns"`
	Statuses      []Status       `json:"statuses"`
	RelationTypes []RelationType `json:"relationship_types"`
	Cards         []Card         `json:"cards"`
	Links         []Link         `json:"links"`
	Attachments   []Attachment   `json:"attachments"`
	Revision      uint64         `json:"revision"`
}
type Store struct {
	db       *sql.DB
	revision atomic.Uint64
}

func openStore(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	schema := `CREATE TABLE IF NOT EXISTS boards(id INTEGER PRIMARY KEY AUTOINCREMENT,name TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS columns(id INTEGER PRIMARY KEY AUTOINCREMENT,board_id INTEGER NOT NULL REFERENCES boards(id) ON DELETE CASCADE,name TEXT NOT NULL,position INTEGER NOT NULL);
 CREATE TABLE IF NOT EXISTS statuses(id INTEGER PRIMARY KEY AUTOINCREMENT,board_id INTEGER NOT NULL REFERENCES boards(id) ON DELETE CASCADE,name TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS relation_types(id INTEGER PRIMARY KEY AUTOINCREMENT,name TEXT NOT NULL UNIQUE,inverse TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS cards(id INTEGER PRIMARY KEY AUTOINCREMENT,board_id INTEGER NOT NULL REFERENCES boards(id) ON DELETE CASCADE,column_id INTEGER NOT NULL REFERENCES columns(id),status_id INTEGER NOT NULL REFERENCES statuses(id),title TEXT NOT NULL,description TEXT NOT NULL DEFAULT '',tags TEXT NOT NULL DEFAULT '[]',checklist TEXT NOT NULL DEFAULT '[]',due_date TEXT NOT NULL DEFAULT '',archived INTEGER NOT NULL DEFAULT 0,position INTEGER NOT NULL,updated_at TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS links(id INTEGER PRIMARY KEY AUTOINCREMENT,source_id INTEGER NOT NULL REFERENCES cards(id) ON DELETE CASCADE,target_id INTEGER NOT NULL REFERENCES cards(id) ON DELETE CASCADE,type_id INTEGER NOT NULL REFERENCES relation_types(id),UNIQUE(source_id,target_id,type_id),CHECK(source_id<>target_id));
 CREATE TABLE IF NOT EXISTS attachments(id INTEGER PRIMARY KEY AUTOINCREMENT,card_id INTEGER NOT NULL REFERENCES cards(id) ON DELETE CASCADE,name TEXT NOT NULL,data BLOB NOT NULL);
 CREATE INDEX IF NOT EXISTS cards_order ON cards(column_id,position);
 CREATE TABLE IF NOT EXISTS metadata(key TEXT PRIMARY KEY,value TEXT NOT NULL);
 INSERT OR IGNORE INTO relation_types(id,name,inverse) SELECT 1,'related to','related to' WHERE NOT EXISTS(SELECT 1 FROM metadata WHERE key='relationships_initialized');
 INSERT OR IGNORE INTO relation_types(id,name,inverse) SELECT 2,'blocks','blocked by' WHERE NOT EXISTS(SELECT 1 FROM metadata WHERE key='relationships_initialized');
 INSERT OR IGNORE INTO relation_types(id,name,inverse) SELECT 3,'duplicates','duplicates' WHERE NOT EXISTS(SELECT 1 FROM metadata WHERE key='relationships_initialized');
 INSERT OR IGNORE INTO metadata(key,value) VALUES('relationships_initialized','1');`
	if _, err = db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	s := &Store{db: db}
	return s, nil
}

func (s *Store) snapshot(ctx context.Context) (Snapshot, error) {
	v := Snapshot{Boards: []Board{}, Columns: []Lane{}, Statuses: []Status{}, RelationTypes: []RelationType{}, Cards: []Card{}, Links: []Link{}, Attachments: []Attachment{}}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return v, err
	}
	defer tx.Rollback()
	queries := []struct {
		q    string
		scan func(*sql.Rows) error
	}{
		{"SELECT id,name FROM boards ORDER BY id", func(r *sql.Rows) error {
			var x Board
			if e := r.Scan(&x.ID, &x.Name); e != nil {
				return e
			}
			v.Boards = append(v.Boards, x)
			return nil
		}},
		{"SELECT id,board_id,name,position FROM columns ORDER BY board_id,position,id", func(r *sql.Rows) error {
			var x Lane
			if e := r.Scan(&x.ID, &x.BoardID, &x.Name, &x.Position); e != nil {
				return e
			}
			v.Columns = append(v.Columns, x)
			return nil
		}},
		{"SELECT id,board_id,name FROM statuses ORDER BY id", func(r *sql.Rows) error {
			var x Status
			if e := r.Scan(&x.ID, &x.BoardID, &x.Name); e != nil {
				return e
			}
			v.Statuses = append(v.Statuses, x)
			return nil
		}},
		{"SELECT id,name,inverse FROM relation_types ORDER BY id", func(r *sql.Rows) error {
			var x RelationType
			if e := r.Scan(&x.ID, &x.Name, &x.Inverse); e != nil {
				return e
			}
			v.RelationTypes = append(v.RelationTypes, x)
			return nil
		}},
		{"SELECT id,board_id,column_id,status_id,title,description,tags,checklist,due_date,archived,position,updated_at FROM cards ORDER BY column_id,position,id", func(r *sql.Rows) error {
			var x Card
			var tags, checks string
			if e := r.Scan(&x.ID, &x.BoardID, &x.ColumnID, &x.StatusID, &x.Title, &x.Description, &tags, &checks, &x.DueDate, &x.Archived, &x.Position, &x.UpdatedAt); e != nil {
				return e
			}
			if e := json.Unmarshal([]byte(tags), &x.Tags); e != nil {
				return e
			}
			if e := json.Unmarshal([]byte(checks), &x.Checklist); e != nil {
				return e
			}
			v.Cards = append(v.Cards, x)
			return nil
		}},
		{"SELECT id,source_id,target_id,type_id FROM links ORDER BY id", func(r *sql.Rows) error {
			var x Link
			if e := r.Scan(&x.ID, &x.SourceID, &x.TargetID, &x.TypeID); e != nil {
				return e
			}
			v.Links = append(v.Links, x)
			return nil
		}},
		{"SELECT id,card_id,name,length(data) FROM attachments ORDER BY id", func(r *sql.Rows) error {
			var x Attachment
			if e := r.Scan(&x.ID, &x.CardID, &x.Name, &x.Size); e != nil {
				return e
			}
			v.Attachments = append(v.Attachments, x)
			return nil
		}},
	}
	for _, q := range queries {
		rows, e := tx.QueryContext(ctx, q.q)
		if e != nil {
			return v, e
		}
		for rows.Next() {
			if e = q.scan(rows); e != nil {
				rows.Close()
				return v, e
			}
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return v, e
		}
	}
	v.Revision = s.revision.Load()
	err = tx.Commit()
	return v, err
}

// Arguments are shared by HTTP forms and MCP. Pointer fields distinguish omitted values from clearing a field.
type Args struct {
	ID            int64     `json:"id,omitempty"`
	BoardID       int64     `json:"board_id,omitempty"`
	ColumnID      int64     `json:"column_id,omitempty"`
	StatusID      int64     `json:"status_id,omitempty"`
	Name          *string   `json:"name,omitempty"`
	Inverse       *string   `json:"inverse,omitempty"`
	Title         *string   `json:"title,omitempty"`
	Description   *string   `json:"description,omitempty"`
	Tags          *[]string `json:"tags,omitempty"`
	Checklist     *[]Check  `json:"checklist,omitempty"`
	DueDate       *string   `json:"due_date,omitempty"`
	Archived      *bool     `json:"archived,omitempty"`
	Position      *int      `json:"position,omitempty"`
	IDs           []int64   `json:"ids,omitempty"`
	SourceID      int64     `json:"source_id,omitempty"`
	TargetID      int64     `json:"target_id,omitempty"`
	TypeID        int64     `json:"type_id,omitempty"`
	ReplacementID int64     `json:"replacement_id,omitempty"`
	Data          *string   `json:"data_base64,omitempty"`
	Query         string    `json:"query,omitempty"`
	Tag           string    `json:"tag,omitempty"`
}

func str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
func ptr[T any](v T) *T { return &v }
func requireName(p *string) error {
	if strings.TrimSpace(str(p)) == "" {
		return errors.New("name cannot be empty")
	}
	if len(str(p)) > 300 {
		return errors.New("name is too long")
	}
	return nil
}
func affected(r sql.Result, e error) error {
	if e != nil {
		return e
	}
	n, e := r.RowsAffected()
	if e != nil {
		return e
	}
	if n == 0 {
		return errors.New("not found")
	}
	return nil
}
func owner(tx *sql.Tx, table string, id int64) (int64, error) {
	var b int64
	e := tx.QueryRow("SELECT board_id FROM "+table+" WHERE id=?", id).Scan(&b)
	if errors.Is(e, sql.ErrNoRows) {
		e = fmt.Errorf("%s %d not found", table, id)
	}
	return b, e
}
func sameBoard(tx *sql.Tx, table string, id, board int64) error {
	b, e := owner(tx, table, id)
	if e != nil {
		return e
	}
	if b != board {
		return fmt.Errorf("%s must belong to board %d", table, board)
	}
	return nil
}
func cardInTx(tx *sql.Tx, id int64) (Card, error) {
	var x Card
	var tags, checks string
	e := tx.QueryRow("SELECT id,board_id,column_id,status_id,title,description,tags,checklist,due_date,archived,position,updated_at FROM cards WHERE id=?", id).Scan(&x.ID, &x.BoardID, &x.ColumnID, &x.StatusID, &x.Title, &x.Description, &tags, &checks, &x.DueDate, &x.Archived, &x.Position, &x.UpdatedAt)
	if errors.Is(e, sql.ErrNoRows) {
		return x, errors.New("card not found")
	}
	if e != nil {
		return x, e
	}
	if e = json.Unmarshal([]byte(tags), &x.Tags); e != nil {
		return x, e
	}
	e = json.Unmarshal([]byte(checks), &x.Checklist)
	return x, e
}
func orderedCards(tx *sql.Tx, col int64, archived bool, exclude int64) ([]int64, error) {
	rows, e := tx.Query("SELECT id FROM cards WHERE column_id=? AND archived=? AND id<>? ORDER BY position,id", col, archived, exclude)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	ids := []int64{}
	for rows.Next() {
		var id int64
		if e = rows.Scan(&id); e != nil {
			return nil, e
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
func writeOrder(tx *sql.Tx, table string, ids []int64) error {
	for i, id := range ids {
		if _, e := tx.Exec("UPDATE "+table+" SET position=? WHERE id=?", i, id); e != nil {
			return e
		}
	}
	return nil
}

func (s *Store) execute(ctx context.Context, op string, a Args) (any, error) {
	if op == "list_workspace" || op == "list_cards" || op == "get_card" {
		v, e := s.snapshot(ctx)
		if e != nil {
			return nil, e
		}
		if op == "list_workspace" {
			return v, nil
		}
		if op == "get_card" {
			for _, c := range v.Cards {
				if c.ID == a.ID {
					links := []Link{}
					files := []Attachment{}
					for _, l := range v.Links {
						if l.SourceID == c.ID || l.TargetID == c.ID {
							links = append(links, l)
						}
					}
					for _, f := range v.Attachments {
						if f.CardID == c.ID {
							files = append(files, f)
						}
					}
					return map[string]any{"card": c, "links": links, "attachments": files}, nil
				}
			}
			return nil, errors.New("card not found")
		}
		cards := []Card{}
		for _, c := range v.Cards {
			if a.BoardID != 0 && c.BoardID != a.BoardID || a.ColumnID != 0 && c.ColumnID != a.ColumnID || a.StatusID != 0 && c.StatusID != a.StatusID || a.Archived != nil && c.Archived != *a.Archived {
				continue
			}
			if a.Query != "" && !strings.Contains(strings.ToLower(c.Title+" "+c.Description+" "+strings.Join(c.Tags, " ")), strings.ToLower(a.Query)) {
				continue
			}
			if a.Tag != "" {
				found := false
				for _, t := range c.Tags {
					if t == a.Tag {
						found = true
					}
				}
				if !found {
					continue
				}
			}
			cards = append(cards, c)
		}
		return map[string]any{"cards": cards}, nil
	}
	if op == "get_attachment" {
		var name string
		var data []byte
		e := s.db.QueryRowContext(ctx, "SELECT name,data FROM attachments WHERE id=?", a.ID).Scan(&name, &data)
		if e != nil {
			return nil, e
		}
		return map[string]any{"name": name, "data_base64": base64.StdEncoding.EncodeToString(data)}, nil
	}
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	var result any = map[string]any{"ok": true}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	switch op {
	case "create_board":
		if e = requireName(a.Name); e != nil {
			return nil, e
		}
		r, e := tx.Exec("INSERT INTO boards(name) VALUES(?)", strings.TrimSpace(*a.Name))
		if e != nil {
			return nil, e
		}
		id, e := r.LastInsertId()
		if e != nil {
			return nil, e
		}
		for i, name := range []string{"Backlog", "In progress", "Done"} {
			if _, e = tx.Exec("INSERT INTO columns(board_id,name,position) VALUES(?,?,?)", id, name, i); e != nil {
				return nil, e
			}
		}
		for _, name := range []string{"Todo", "In progress", "Done"} {
			if _, e = tx.Exec("INSERT INTO statuses(board_id,name) VALUES(?,?)", id, name); e != nil {
				return nil, e
			}
		}
		result = Board{ID: id, Name: strings.TrimSpace(*a.Name)}
	case "update_board":
		if e = requireName(a.Name); e == nil {
			e = affected(tx.Exec("UPDATE boards SET name=? WHERE id=?", strings.TrimSpace(*a.Name), a.ID))
		}
	case "delete_board":
		// Delete cards first because their lane/status foreign keys intentionally restrict deletion.
		if _, e = tx.Exec("DELETE FROM cards WHERE board_id=?", a.ID); e == nil {
			e = affected(tx.Exec("DELETE FROM boards WHERE id=?", a.ID))
		}
	case "create_column", "create_status":
		if e = requireName(a.Name); e != nil {
			return nil, e
		}
		var r sql.Result
		if op == "create_column" {
			r, e = tx.Exec("INSERT INTO columns(board_id,name,position) SELECT ?,?,COALESCE(MAX(position),-1)+1 FROM columns WHERE board_id=?", a.BoardID, strings.TrimSpace(*a.Name), a.BoardID)
		} else {
			r, e = tx.Exec("INSERT INTO statuses(board_id,name) VALUES(?,?)", a.BoardID, strings.TrimSpace(*a.Name))
		}
		if e == nil {
			id, err := r.LastInsertId()
			e = err
			result = map[string]any{"id": id}
		}
	case "update_column", "update_status":
		if e = requireName(a.Name); e == nil {
			table := "columns"
			if op == "update_status" {
				table = "statuses"
			}
			e = affected(tx.Exec("UPDATE "+table+" SET name=? WHERE id=?", strings.TrimSpace(*a.Name), a.ID))
		}
	case "delete_column", "delete_status":
		table, field := "columns", "column_id"
		if op == "delete_status" {
			table, field = "statuses", "status_id"
		}
		b, err := owner(tx, table, a.ID)
		if err != nil {
			return nil, err
		}
		var count int
		if e = tx.QueryRow("SELECT count(*) FROM "+table+" WHERE board_id=?", b).Scan(&count); e != nil {
			return nil, e
		}
		if count <= 1 {
			return nil, fmt.Errorf("a board needs at least one %s", strings.TrimSuffix(table, "s"))
		}
		if a.ReplacementID == a.ID {
			return nil, errors.New("replacement must be different")
		}
		if a.ReplacementID != 0 {
			if e = sameBoard(tx, table, a.ReplacementID, b); e != nil {
				return nil, e
			}
			if _, e = tx.Exec("UPDATE cards SET "+field+"=?,updated_at=? WHERE "+field+"=?", a.ReplacementID, now, a.ID); e != nil {
				return nil, e
			}
		}
		e = affected(tx.Exec("DELETE FROM "+table+" WHERE id=?", a.ID))
		if e != nil {
			return nil, errors.New("move cards to a replacement before deleting this item")
		}
		if table == "columns" && a.ReplacementID != 0 {
			for _, arch := range []bool{false, true} {
				ids, err := orderedCards(tx, a.ReplacementID, arch, 0)
				if err != nil {
					return nil, err
				}
				if err = writeOrder(tx, "cards", ids); err != nil {
					return nil, err
				}
			}
		}
	case "reorder_columns":
		rows, err := tx.Query("SELECT id FROM columns WHERE board_id=?", a.BoardID)
		if err != nil {
			return nil, err
		}
		known := map[int64]bool{}
		for rows.Next() {
			var id int64
			if err = rows.Scan(&id); err != nil {
				rows.Close()
				return nil, err
			}
			known[id] = true
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
		if len(a.IDs) != len(known) || len(known) == 0 {
			return nil, errors.New("provide every column ID for this board exactly once")
		}
		for _, id := range a.IDs {
			if !known[id] {
				return nil, errors.New("unknown or duplicate column")
			}
			delete(known, id)
		}
		e = writeOrder(tx, "columns", a.IDs)
	case "create_card", "update_card", "move_card":
		c := Card{BoardID: a.BoardID, Tags: []string{}, Checklist: []Check{}}
		if op != "create_card" {
			c, e = cardInTx(tx, a.ID)
			if e != nil {
				return nil, e
			}
		}
		oldCol, oldArch := c.ColumnID, c.Archived
		if a.ColumnID != 0 {
			b, err := owner(tx, "columns", a.ColumnID)
			if err != nil {
				return nil, err
			}
			if a.BoardID != 0 && a.BoardID != b {
				return nil, errors.New("column and board do not match")
			}
			c.BoardID = b
			c.ColumnID = a.ColumnID
		}
		if c.ColumnID == 0 {
			if e = tx.QueryRow("SELECT id FROM columns WHERE board_id=? ORDER BY position,id LIMIT 1", c.BoardID).Scan(&c.ColumnID); e != nil {
				return nil, errors.New("board not found")
			}
		}
		if a.StatusID != 0 {
			c.StatusID = a.StatusID
		}
		if c.StatusID == 0 {
			if e = tx.QueryRow("SELECT id FROM statuses WHERE board_id=? ORDER BY id LIMIT 1", c.BoardID).Scan(&c.StatusID); e != nil {
				return nil, e
			}
		}
		if e = sameBoard(tx, "statuses", c.StatusID, c.BoardID); e != nil {
			return nil, errors.New("status must belong to the destination board; specify status_id when moving between boards")
		}
		if a.Title != nil {
			c.Title = strings.TrimSpace(*a.Title)
		}
		if c.Title == "" {
			return nil, errors.New("title cannot be empty")
		}
		if len(c.Title) > 500 {
			return nil, errors.New("title is too long")
		}
		if a.Description != nil {
			c.Description = *a.Description
		}
		if a.DueDate != nil {
			c.DueDate = *a.DueDate
		}
		if c.DueDate != "" {
			if _, e = time.Parse("2006-01-02", c.DueDate); e != nil {
				return nil, errors.New("due_date must be YYYY-MM-DD or empty")
			}
		}
		if a.Tags != nil {
			c.Tags = []string{}
			seen := map[string]bool{}
			for _, t := range *a.Tags {
				t = strings.TrimSpace(t)
				if t != "" && !seen[t] {
					c.Tags = append(c.Tags, t)
					seen[t] = true
				}
			}
		}
		if a.Checklist != nil {
			c.Checklist = []Check{}
			for _, ch := range *a.Checklist {
				ch.Text = strings.TrimSpace(ch.Text)
				if ch.Text != "" {
					c.Checklist = append(c.Checklist, ch)
				}
			}
		}
		if a.Archived != nil {
			c.Archived = *a.Archived
		}
		tags, _ := json.Marshal(c.Tags)
		checks, _ := json.Marshal(c.Checklist)
		if op == "create_card" {
			r, err := tx.Exec("INSERT INTO cards(board_id,column_id,status_id,title,description,tags,checklist,due_date,archived,position,updated_at) VALUES(?,?,?,?,?,?,?,?,?,0,?)", c.BoardID, c.ColumnID, c.StatusID, c.Title, c.Description, string(tags), string(checks), c.DueDate, c.Archived, now)
			if err != nil {
				return nil, err
			}
			c.ID, e = r.LastInsertId()
			if e != nil {
				return nil, e
			}
		} else {
			_, e = tx.Exec("UPDATE cards SET board_id=?,column_id=?,status_id=?,title=?,description=?,tags=?,checklist=?,due_date=?,archived=?,updated_at=? WHERE id=?", c.BoardID, c.ColumnID, c.StatusID, c.Title, c.Description, string(tags), string(checks), c.DueDate, c.Archived, now, c.ID)
			if e != nil {
				return nil, e
			}
		}
		if op == "create_card" || oldCol != c.ColumnID || oldArch != c.Archived || a.Position != nil {
			ids, err := orderedCards(tx, c.ColumnID, c.Archived, c.ID)
			if err != nil {
				return nil, err
			}
			pos := len(ids)
			if a.Position != nil {
				pos = *a.Position
				if pos < 0 || pos > len(ids) {
					return nil, errors.New("position is outside the destination column")
				}
			}
			ids = append(ids, 0)
			copy(ids[pos+1:], ids[pos:])
			ids[pos] = c.ID
			if e = writeOrder(tx, "cards", ids); e != nil {
				return nil, e
			}
			c.Position = pos
			if oldCol != 0 && (oldCol != c.ColumnID || oldArch != c.Archived) {
				ids, err = orderedCards(tx, oldCol, oldArch, 0)
				if err != nil {
					return nil, err
				}
				if e = writeOrder(tx, "cards", ids); e != nil {
					return nil, e
				}
			}
		}
		c.UpdatedAt = now
		result = c
	case "delete_card":
		card, err := cardInTx(tx, a.ID)
		if err != nil {
			return nil, err
		}
		if e = affected(tx.Exec("DELETE FROM cards WHERE id=?", a.ID)); e != nil {
			return nil, e
		}
		ids, err := orderedCards(tx, card.ColumnID, card.Archived, 0)
		if err != nil {
			return nil, err
		}
		e = writeOrder(tx, "cards", ids)
	case "create_relationship_type":
		if e = requireName(a.Name); e != nil {
			return nil, e
		}
		inverse := str(a.Inverse)
		if strings.TrimSpace(inverse) == "" {
			inverse = strings.TrimSpace(*a.Name)
		}
		r, err := tx.Exec("INSERT INTO relation_types(name,inverse) VALUES(?,?)", strings.TrimSpace(*a.Name), inverse)
		e = err
		if e == nil {
			id, err := r.LastInsertId()
			e = err
			result = map[string]any{"id": id}
		}
	case "update_relationship_type":
		if e = requireName(a.Name); e != nil {
			return nil, e
		}
		inverse := str(a.Inverse)
		if inverse == "" {
			inverse = *a.Name
		}
		e = affected(tx.Exec("UPDATE relation_types SET name=?,inverse=? WHERE id=?", *a.Name, inverse, a.ID))
	case "delete_relationship_type":
		if a.ReplacementID == a.ID {
			return nil, errors.New("replacement must be different")
		}
		if a.ReplacementID != 0 {
			if _, e = tx.Exec("UPDATE links SET type_id=? WHERE type_id=?", a.ReplacementID, a.ID); e != nil {
				return nil, e
			}
		}
		e = affected(tx.Exec("DELETE FROM relation_types WHERE id=?", a.ID))
		if e != nil {
			return nil, errors.New("remove or retype links before deleting this relationship type")
		}
	case "create_link":
		if a.SourceID == a.TargetID {
			return nil, errors.New("a card cannot link to itself")
		}
		r, err := tx.Exec("INSERT INTO links(source_id,target_id,type_id) VALUES(?,?,?)", a.SourceID, a.TargetID, a.TypeID)
		e = err
		if e == nil {
			id, err := r.LastInsertId()
			e = err
			result = Link{ID: id, SourceID: a.SourceID, TargetID: a.TargetID, TypeID: a.TypeID}
		}
	case "delete_link":
		e = affected(tx.Exec("DELETE FROM links WHERE id=?", a.ID))
	case "create_attachment":
		if e = requireName(a.Name); e != nil {
			return nil, e
		}
		data, err := base64.StdEncoding.DecodeString(str(a.Data))
		if err != nil {
			return nil, errors.New("invalid base64 attachment")
		}
		if len(data) > 10<<20 {
			return nil, errors.New("attachment limit is 10 MB")
		}
		r, err := tx.Exec("INSERT INTO attachments(card_id,name,data) VALUES(?,?,?)", a.ID, *a.Name, data)
		e = err
		if e == nil {
			id, err := r.LastInsertId()
			e = err
			result = Attachment{ID: id, CardID: a.ID, Name: *a.Name, Size: len(data)}
		}
	case "delete_attachment":
		e = affected(tx.Exec("DELETE FROM attachments WHERE id=?", a.ID))
	default:
		return nil, errors.New("unknown operation")
	}
	if e != nil {
		return nil, e
	}
	if e = tx.Commit(); e != nil {
		return nil, e
	}
	s.revision.Add(1)
	return result, nil
}
