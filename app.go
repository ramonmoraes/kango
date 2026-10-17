package main

import (
	"bytes"
	"embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
)

//go:embed web
var assets embed.FS

type App struct {
	s        *Store
	t        *template.Template
	markdown goldmark.Markdown
}
type ColumnView struct {
	Lane
	Cards []Card
}
type LinkView struct {
	ID    int64
	Label string
	Card  Card
}
type Page struct {
	Snapshot
	Board           Board
	ColumnsView     []ColumnView
	BoardStatuses   []Status
	Tags            []string
	Card            *Card
	CardLinks       []LinkView
	CardFiles       []Attachment
	Modal           string
	EditingID       int64
	Entity          string
	EntityName      string
	Inverse         string
	Query           string
	Tag             string
	StatusID        int64
	Archived        bool
	ReturnURL       string
	ChecklistText   string
	DescriptionHTML template.HTML
	ReadOnly        bool
}

func newApp(s *Store) (*App, error) {
	a := &App{s: s, markdown: goldmark.New(goldmark.WithExtensions(extension.GFM))}
	funcs := template.FuncMap{"statusName": func(statuses []Status, id int64) string {
		for _, x := range statuses {
			if x.ID == id {
				return x.Name
			}
		}
		return "Unknown"
	}, "boardName": func(boards []Board, id int64) string {
		for _, x := range boards {
			if x.ID == id {
				return x.Name
			}
		}
		return "Unknown"
	}, "modalURL": func(base, modal, entity string, id int64) string {
		u, _ := url.Parse(base)
		q := u.Query()
		q.Set("modal", modal)
		if entity != "" {
			q.Set("entity", entity)
		}
		if id != 0 {
			q.Set("edit", strconv.FormatInt(id, 10))
		}
		u.RawQuery = q.Encode()
		return u.String()
	}, "join": strings.Join, "doneCount": func(checks []Check) int {
		n := 0
		for _, c := range checks {
			if c.Done {
				n++
			}
		}
		return n
	}, "initial": func(s string) string {
		r := []rune(strings.TrimSpace(s))
		if len(r) == 0 {
			return "K"
		}
		return strings.ToUpper(string(r[0]))
	}, "fileSize": func(n int) string {
		if n < 1024 {
			return fmt.Sprintf("%d B", n)
		}
		if n < 1<<20 {
			return fmt.Sprintf("%.1f KB", float64(n)/1024)
		}
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	}}
	t, e := template.New("app").Funcs(funcs).ParseFS(assets, "web/*.html")
	a.t = t
	return a, e
}
func number(s string) int64 { v, _ := strconv.ParseInt(s, 10, 64); return v }
func (a *App) handler() http.Handler {
	mux := http.NewServeMux()
	static, _ := fs.Sub(assets, "web/static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(static))))
	mux.Handle("/mcp", a.s.mcpHandler())
	mux.HandleFunc("GET /api/workspace", func(w http.ResponseWriter, r *http.Request) {
		v, e := a.s.snapshot(r.Context())
		if e != nil {
			http.Error(w, e.Error(), 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(v)
	})
	mux.HandleFunc("GET /api/revision", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"revision":%d}`, a.s.revision.Load())
	})
	mux.HandleFunc("POST /actions/{op}", a.action)
	mux.HandleFunc("GET /attachment/{id}", a.download)
	mux.HandleFunc("POST /preview", func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
		if e := r.ParseForm(); e != nil {
			http.Error(w, e.Error(), 400)
			return
		}
		var b bytes.Buffer
		if e := a.markdown.Convert([]byte(r.FormValue("description")), &b); e != nil {
			http.Error(w, e.Error(), 400)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(b.Bytes())
	})
	mux.HandleFunc("GET /{$}", a.page)
	mux.HandleFunc("GET /board/{id}", a.page)
	mux.HandleFunc("GET /card/{id}", a.page)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			r.Body = http.MaxBytesReader(w, r.Body, 15<<20)
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			u, e := url.Parse(origin)
			if e != nil || u.Host != r.Host || (u.Scheme != "http" && u.Scheme != "https") {
				http.Error(w, "cross-origin request rejected", 403)
				return
			}
		}
		if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			http.Error(w, "cross-site request rejected", 403)
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		if !strings.HasPrefix(r.URL.Path, "/static/") {
			w.Header().Set("Cache-Control", "no-store")
		}
		mux.ServeHTTP(w, r)
	})
}
func (a *App) pageData(r *http.Request) (Page, error) {
	v, e := a.s.snapshot(r.Context())
	if e != nil {
		return Page{}, e
	}
	p := Page{Snapshot: v, Modal: r.URL.Query().Get("modal"), EditingID: number(r.URL.Query().Get("edit")), Entity: r.URL.Query().Get("entity"), Query: r.URL.Query().Get("q"), Tag: r.URL.Query().Get("tag"), StatusID: number(r.URL.Query().Get("status")), Archived: r.URL.Query().Get("archived") == "1"}
	boardID := number(r.URL.Query().Get("board"))
	if strings.HasPrefix(r.URL.Path, "/board/") {
		boardID = number(strings.TrimPrefix(r.URL.Path, "/board/"))
	}
	if strings.HasPrefix(r.URL.Path, "/card/") {
		id := number(strings.TrimPrefix(r.URL.Path, "/card/"))
		for _, c := range v.Cards {
			if c.ID == id {
				copy := c
				p.Card = &copy
				p.Modal = "card"
				boardID = c.BoardID
			}
		}
		if p.Card == nil {
			return p, fmt.Errorf("card not found")
		}
	}
	if boardID == 0 && len(v.Boards) > 0 {
		boardID = v.Boards[0].ID
	}
	for _, b := range v.Boards {
		if b.ID == boardID {
			p.Board = b
		}
	}
	if boardID != 0 && p.Board.ID == 0 {
		return p, fmt.Errorf("board not found")
	}
	tagSet := map[string]bool{}
	for _, s := range v.Statuses {
		if s.BoardID == p.Board.ID {
			p.BoardStatuses = append(p.BoardStatuses, s)
		}
	}
	for _, c := range v.Columns {
		if c.BoardID != p.Board.ID {
			continue
		}
		view := ColumnView{Lane: c, Cards: []Card{}}
		for _, card := range v.Cards {
			if card.ColumnID != c.ID {
				continue
			}
			for _, t := range card.Tags {
				if !tagSet[t] {
					p.Tags = append(p.Tags, t)
					tagSet[t] = true
				}
			}
			if card.Archived != p.Archived || p.StatusID != 0 && card.StatusID != p.StatusID {
				continue
			}
			if p.Query != "" && !strings.Contains(strings.ToLower(card.Title+" "+card.Description+" "+strings.Join(card.Tags, " ")), strings.ToLower(p.Query)) {
				continue
			}
			if p.Tag != "" {
				has := false
				for _, t := range card.Tags {
					if t == p.Tag {
						has = true
					}
				}
				if !has {
					continue
				}
			}
			view.Cards = append(view.Cards, card)
		}
		p.ColumnsView = append(p.ColumnsView, view)
	}
	p.ReadOnly = p.Query != "" || p.Tag != "" || p.StatusID != 0
	p.ReturnURL = fmt.Sprintf("/board/%d", p.Board.ID)
	if p.Board.ID == 0 {
		p.ReturnURL = "/"
	}
	q := url.Values{}
	if p.Query != "" {
		q.Set("q", p.Query)
	}
	if p.Tag != "" {
		q.Set("tag", p.Tag)
	}
	if p.StatusID != 0 {
		q.Set("status", strconv.FormatInt(p.StatusID, 10))
	}
	if p.Archived {
		q.Set("archived", "1")
	}
	if len(q) > 0 {
		p.ReturnURL += "?" + q.Encode()
	}
	if p.Modal == "new-card" {
		col := number(r.URL.Query().Get("column"))
		if col == 0 && len(p.ColumnsView) > 0 {
			col = p.ColumnsView[0].ID
		}
		status := int64(0)
		if len(p.BoardStatuses) > 0 {
			status = p.BoardStatuses[0].ID
		}
		p.Card = &Card{BoardID: p.Board.ID, ColumnID: col, StatusID: status, Tags: []string{}, Checklist: []Check{}}
	}
	if p.Card != nil {
		var b bytes.Buffer
		if e = a.markdown.Convert([]byte(p.Card.Description), &b); e != nil {
			return p, e
		}
		p.DescriptionHTML = template.HTML(b.String())
		for _, ch := range p.Card.Checklist {
			mark := "[ ] "
			if ch.Done {
				mark = "[x] "
			}
			p.ChecklistText += mark + ch.Text + "\n"
		}
		for _, l := range v.Links {
			other := int64(0)
			label := ""
			for _, t := range v.RelationTypes {
				if t.ID == l.TypeID {
					if l.SourceID == p.Card.ID {
						other = l.TargetID
						label = t.Name
					} else if l.TargetID == p.Card.ID {
						other = l.SourceID
						label = t.Inverse
					}
				}
			}
			for _, c := range v.Cards {
				if c.ID == other {
					p.CardLinks = append(p.CardLinks, LinkView{ID: l.ID, Label: label, Card: c})
				}
			}
		}
		for _, f := range v.Attachments {
			if f.CardID == p.Card.ID {
				p.CardFiles = append(p.CardFiles, f)
			}
		}
	}
	if p.Modal == "settings" {
		switch p.Entity {
		case "board":
			p.EntityName = p.Board.Name
		case "column":
			for _, c := range v.Columns {
				if c.ID == p.EditingID {
					p.EntityName = c.Name
				}
			}
		case "status":
			for _, s := range v.Statuses {
				if s.ID == p.EditingID {
					p.EntityName = s.Name
				}
			}
		case "relationship_type":
			for _, t := range v.RelationTypes {
				if t.ID == p.EditingID {
					p.EntityName = t.Name
					p.Inverse = t.Inverse
				}
			}
		}
	}
	return p, nil
}
func (a *App) page(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" && !strings.HasPrefix(r.URL.Path, "/board/") && !strings.HasPrefix(r.URL.Path, "/card/") {
		http.NotFound(w, r)
		return
	}
	p, e := a.pageData(r)
	if e != nil {
		http.Error(w, e.Error(), 404)
		return
	}
	name := "document"
	if r.Header.Get("HX-Request") == "true" && r.Header.Get("HX-History-Restore-Request") != "true" {
		name = "workspace"
	}
	a.render(w, name, p)
}
func (a *App) render(w http.ResponseWriter, name string, p Page) {
	var b bytes.Buffer
	if e := a.t.ExecuteTemplate(&b, name, p); e != nil {
		http.Error(w, e.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(b.Bytes())
}
func safeReturn(s string) string {
	if s == "/" || strings.HasPrefix(s, "/board/") || strings.HasPrefix(s, "/card/") {
		u, e := url.Parse(s)
		if e == nil && u.Host == "" && !strings.HasPrefix(s, "//") {
			return s
		}
	}
	return "/"
}
func (a *App) action(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 15<<20)
	var args Args
	op := r.PathValue("op")
	ret := "/"
	open := false
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		if e := dec.Decode(&args); e != nil {
			http.Error(w, e.Error(), 400)
			return
		}
	} else {
		var e error
		if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
			e = r.ParseMultipartForm(1 << 20)
			if r.MultipartForm != nil {
				defer r.MultipartForm.RemoveAll()
			}
		} else {
			e = r.ParseForm()
		}
		if e != nil {
			http.Error(w, e.Error(), 400)
			return
		}
		form := r.PostForm
		args.ID = number(form.Get("id"))
		args.BoardID = number(form.Get("board_id"))
		args.ColumnID = number(form.Get("column_id"))
		args.StatusID = number(form.Get("status_id"))
		args.SourceID = number(form.Get("source_id"))
		args.TargetID = number(form.Get("target_id"))
		args.TypeID = number(form.Get("type_id"))
		args.ReplacementID = number(form.Get("replacement_id"))
		ret = safeReturn(form.Get("return"))
		open = form.Get("open") == "1"
		for key, p := range map[string]**string{"name": &args.Name, "inverse": &args.Inverse, "title": &args.Title, "description": &args.Description, "due_date": &args.DueDate} {
			if form.Has(key) {
				*p = ptr(form.Get(key))
			}
		}
		if form.Has("archived") {
			args.Archived = ptr(form.Get("archived") == "true")
		}
		if form.Has("tags") {
			args.Tags = ptr(strings.Split(form.Get("tags"), ","))
		}
		if form.Has("checklist") {
			checks := []Check{}
			for _, line := range strings.Split(form.Get("checklist"), "\n") {
				line = strings.TrimSpace(line)
				if line == "" {
					continue
				}
				done := strings.HasPrefix(strings.ToLower(line), "[x]")
				if strings.HasPrefix(line, "[ ]") || done {
					line = strings.TrimSpace(line[3:])
				}
				checks = append(checks, Check{Text: line, Done: done})
			}
			args.Checklist = &checks
		}
		if op == "create_attachment" {
			f, h, e := r.FormFile("file")
			if e != nil {
				http.Error(w, "choose a file", 400)
				return
			}
			defer f.Close()
			data, e := io.ReadAll(io.LimitReader(f, (10<<20)+1))
			if e != nil || len(data) > 10<<20 {
				http.Error(w, "attachment limit is 10 MB", 400)
				return
			}
			args.Name = ptr(filepath.Base(h.Filename))
			args.Data = ptr(base64.StdEncoding.EncodeToString(data))
		}
	}
	out, e := a.s.execute(r.Context(), op, args)
	if e != nil {
		http.Error(w, e.Error(), 400)
		return
	}
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(out)
		return
	}
	if c, ok := out.(Card); ok && open {
		ret = fmt.Sprintf("/card/%d", c.ID)
	}
	if b, ok := out.(Board); ok {
		ret = fmt.Sprintf("/board/%d", b.ID)
	}
	if r.Header.Get("HX-Request") == "true" {
		u, _ := url.Parse(ret)
		r.URL = u
		p, e := a.pageData(r)
		if e != nil {
			r.URL = &url.URL{Path: "/"}
			p, e = a.pageData(r)
		}
		if e != nil {
			http.Error(w, e.Error(), 500)
			return
		}
		w.Header().Set("HX-Replace-Url", r.URL.String())
		a.render(w, "workspace", p)
	} else {
		http.Redirect(w, r, ret, 303)
	}
}
func (a *App) download(w http.ResponseWriter, r *http.Request) {
	var name string
	var data []byte
	if e := a.s.db.QueryRowContext(r.Context(), "SELECT name,data FROM attachments WHERE id=?", number(r.PathValue("id"))).Scan(&name, &data); e != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename*=UTF-8''%s", url.PathEscape(name)))
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Write(data)
}
