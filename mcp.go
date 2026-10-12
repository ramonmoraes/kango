package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type toolSpec struct{ Name, Description, Fields, Required string }

var toolSpecs = []toolSpec{
	{"list_workspace", "Read all boards, columns, statuses, cards, links, relationship types and attachment metadata. Start here to discover IDs.", "", ""},
	{"list_cards", "Search cards across boards. Omitted archived includes both active and archived cards.", "board_id column_id status_id query tag archived", ""},
	{"get_card", "Read a card including checklist, tags, incoming/outgoing links and attachments.", "id", "id"},
	{"create_board", "Create a board with three starter columns and three independent statuses.", "name", "name"},
	{"update_board", "Rename a board.", "id name", "id name"},
	{"delete_board", "Permanently delete a board and all its cards, links, attachments, columns and statuses.", "id", "id"},
	{"create_column", "Add a column at the end of a board.", "board_id name", "board_id name"},
	{"update_column", "Rename a column.", "id name", "id name"},
	{"delete_column", "Delete a column. If it contains cards, provide replacement_id for a column in the same board. Keep at least one column.", "id replacement_id", "id"},
	{"reorder_columns", "Reorder a board's columns. Supply every column ID exactly once in desired order.", "board_id ids", "board_id ids"},
	{"create_status", "Create an independent card status in a board.", "board_id name", "board_id name"},
	{"update_status", "Rename a status.", "id name", "id name"},
	{"delete_status", "Delete a status. If it is used, provide replacement_id for a status in the same board. Keep at least one status.", "id replacement_id", "id"},
	{"create_card", "Create a card. Column and status default to the first in the board. Position is a zero-based index, default at end. Tags are generic strings. Checklist items have text and done.", "board_id column_id status_id title description tags checklist due_date archived position", "board_id title"},
	{"update_card", "Update only supplied fields. Empty description/due_date and empty tags/checklist clear those fields. Archive/restore via archived. Specify destination column_id and status_id to transfer between boards.", "id column_id status_id title description tags checklist due_date archived position", "id"},
	{"move_card", "Move/reorder a card into a column at zero-based position (default at end when changing column). Moving does not change status. For cross-board moves specify destination status_id.", "id column_id position status_id", "id column_id"},
	{"delete_card", "Permanently delete a card and all its links and attachments.", "id", "id"},
	{"create_relationship_type", "Define a typed card relationship, with optional reverse wording. For simple links use related to.", "name inverse", "name"},
	{"update_relationship_type", "Rename a relationship type and its reverse wording.", "id name inverse", "id name"},
	{"delete_relationship_type", "Delete a relationship type. Remove existing links or provide replacement_id first.", "id replacement_id", "id"},
	{"create_link", "Link source_id to target_id using type_id. Incoming links use the type's inverse wording. Links may cross boards.", "source_id target_id type_id", "source_id target_id type_id"},
	{"delete_link", "Remove a link between cards.", "id", "id"},
	{"create_attachment", "Attach a file to card id. Provide name and base64-encoded bytes. Maximum decoded size 10 MB.", "id name data_base64", "id name data_base64"},
	{"get_attachment", "Read an attachment's filename and base64-encoded bytes.", "id", "id"},
	{"delete_attachment", "Delete an attachment by its attachment id.", "id", "id"},
}

func (s *Store) mcpHandler() http.Handler {
	server := mcp.NewServer(&mcp.Implementation{Name: "kango", Version: "1.0.0"}, &mcp.ServerOptions{Instructions: "Kango is a local single-user kanban workspace. Use list_workspace to discover IDs. Columns and statuses are independent. Relationship types are global; columns and statuses belong to boards. All mutations persist to SQLite and appear in the browser automatically. Destructive delete tools permanently remove data. Card descriptions support Markdown. Due dates use YYYY-MM-DD."})
	for _, spec := range toolSpecs {
		props := map[string]*jsonschema.Schema{}
		for _, field := range strings.Fields(spec.Fields) {
			p := &jsonschema.Schema{Type: "string"}
			switch field {
			case "id", "board_id", "column_id", "status_id", "replacement_id", "source_id", "target_id", "type_id":
				p.Type = "integer"
				p.Minimum = ptr(float64(1))
			case "position":
				p.Type = "integer"
				p.Minimum = ptr(float64(0))
				p.Description = "Zero-based position among cards in the destination column with the same archived state."
			case "archived":
				p.Type = "boolean"
			case "tags":
				p.Type = "array"
				p.Items = &jsonschema.Schema{Type: "string"}
			case "ids":
				p.Type = "array"
				p.Items = &jsonschema.Schema{Type: "integer", Minimum: ptr(float64(1))}
				p.UniqueItems = true
			case "checklist":
				p.Type = "array"
				p.Items = &jsonschema.Schema{Type: "object", Properties: map[string]*jsonschema.Schema{"text": {Type: "string"}, "done": {Type: "boolean"}}, Required: []string{"text", "done"}, AdditionalProperties: &jsonschema.Schema{Not: &jsonschema.Schema{}}}
			case "due_date":
				p.Description = "YYYY-MM-DD, or an empty string to clear."
			}
			props[field] = p
		}
		schema := &jsonschema.Schema{Type: "object", Properties: props, Required: strings.Fields(spec.Required), AdditionalProperties: &jsonschema.Schema{Not: &jsonschema.Schema{}}}
		resolved, err := schema.Resolve(nil)
		if err != nil {
			panic(fmt.Sprintf("tool %s: %v", spec.Name, err))
		}
		readOnly := strings.HasPrefix(spec.Name, "list_") || strings.HasPrefix(spec.Name, "get_")
		annotations := &mcp.ToolAnnotations{ReadOnlyHint: readOnly, DestructiveHint: ptr(!readOnly && !strings.HasPrefix(spec.Name, "create_")), IdempotentHint: readOnly || strings.HasPrefix(spec.Name, "update_") || spec.Name == "reorder_columns", OpenWorldHint: ptr(false)}
		server.AddTool(&mcp.Tool{Name: spec.Name, Description: spec.Description, InputSchema: schema, Annotations: annotations}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			fail := func(err error) (*mcp.CallToolResult, error) {
				return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}}}, nil
			}
			raw := req.Params.Arguments
			if len(raw) == 0 {
				raw = json.RawMessage(`{}`)
			}
			var input any
			if err := json.Unmarshal(raw, &input); err != nil {
				return fail(err)
			}
			if err := resolved.Validate(input); err != nil {
				return fail(err)
			}
			var args Args
			if err := json.Unmarshal(raw, &args); err != nil {
				return fail(err)
			}
			out, err := s.execute(ctx, spec.Name, args)
			if err != nil {
				return fail(err)
			}
			data, err := json.Marshal(out)
			if err != nil {
				return fail(err)
			}
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(data)}}, StructuredContent: out}, nil
		})
	}
	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
}
