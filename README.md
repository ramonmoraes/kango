# Kango

A local, single-user kanban app built with Go, HTMX, and SQLite. A minimal black-and-white interface with a generic MCP server at `/mcp`.

## Run

Requires Go 1.25+ and a C compiler (the SQLite driver uses CGO; macOS Command Line Tools provide it).

```sh
go run .
```

Open <http://localhost:2207>. Create your first board to get started. The app starts empty and stores cards and attachment bytes in `data/kango.db`. Browser assets are bundled locally, so runtime use does not require internet access.

```sh
go run . -addr 127.0.0.1:2207 -db data/kango.db
make build
./kango
make test
```

The app binds to loopback by default and accepts only localhost/loopback hosts and same-origin browser requests. It is designed for local use without accounts.

## Features

- Multiple boards; create, rename, and delete boards, columns, and statuses.
- Drag cards between columns, reorder cards, and drag column handles to reorder columns. Touch dragging is supported. Clear search/filter selections before reordering cards, so positions refer to the complete column.
- Independent column and status fields. Moving a card does not change its status. Use the card editor to transfer cards between boards and choose a status in the destination board.
- Card titles, Markdown descriptions with safe preview, generic tags, due dates, checklists, and file attachments up to 10 MB each.
- Simple links and typed connections, including `related to`, `blocks / blocked by`, and `duplicates`. Add custom types and reverse wording in settings. Incoming and outgoing links appear in both card editors, including connections between boards.
- Search titles, descriptions, and tags; filter by status and tag; archive and restore cards.
- `/card/123` opens the full app with card 123 in an edit modal. HTMX navigation updates the URL; browser Back closes an opened modal and Forward reopens it. Escape and the backdrop close dialogs. Unsaved card changes receive a discard prompt when following links or running another card action.
- MCP changes refresh the board automatically within about 2.5 seconds. Open dialogs retain their current contents and show an update notice to preserve unsaved work. Save commits changes; simultaneous edits to the same card use the last saved values.

Deleting a board/card is permanent and cascades to its links and attachments. Deleting an occupied column or a used status requires a replacement in the same board. Each board keeps at least one column and status. Removing a relationship type requires removing its links or selecting a replacement through MCP.

## Connect Codex

Keep Kango running, then register the endpoint:

```sh
codex mcp add kango --url http://127.0.0.1:2207/mcp
```

Or add this to your Codex MCP configuration:

```toml
[mcp_servers.kango]
url = "http://127.0.0.1:2207/mcp"
```

Restart the client after configuring it. See the [official OpenAI MCP documentation](https://developers.openai.com/codex/mcp) for client configuration. No OAuth or API key is needed for this local server.

Example prompt: “Use Kango to create a Release board, add a Research card tagged planning, and link it as blocking an Implementation card.”

## Generic MCP clients

Any MCP client supporting Streamable HTTP can connect to `http://127.0.0.1:2207/mcp`. The endpoint uses the [official Go MCP SDK](https://github.com/modelcontextprotocol/go-sdk), standard JSON-RPC, stateless HTTP transport, and JSON responses. It has no Codex-specific protocol behavior. Clients limited to stdio need an HTTP bridge.

The server exposes 25 tools with individual input schemas, tool errors, and both text and structured results:

| Scope | Tools |
| --- | --- |
| Read | `list_workspace`, `list_cards`, `get_card` |
| Boards | `create_board`, `update_board`, `delete_board` |
| Columns | `create_column`, `update_column`, `delete_column`, `reorder_columns` |
| Statuses | `create_status`, `update_status`, `delete_status` |
| Cards | `create_card`, `update_card`, `move_card`, `delete_card` |
| Relationship types | `create_relationship_type`, `update_relationship_type`, `delete_relationship_type` |
| Connections | `create_link`, `delete_link` |
| Attachments | `create_attachment`, `get_attachment`, `delete_attachment` |

Start with `list_workspace` to discover IDs. `update_card` changes only supplied fields; empty strings clear description/due date, and empty arrays clear tags/checklists. Archive or restore with `archived: true/false`. Reordering positions are zero-based among cards in the destination column with the same archive state. `list_cards` accepts `board_id`, `column_id`, `status_id`, `query`, `tag`, and `archived` filters; omitting `archived` includes both states.

Attachment tools use base64-encoded file contents. Relationship links use `source_id`, `target_id`, and `type_id`; the inverse wording is displayed on the target card. IDs are never reused after deletion.

## Persistence and development

SQLite uses foreign keys, WAL mode, transactions, and a busy timeout. All UI and MCP mutations use the same validation and storage operations. Stop the app before copying the SQLite database for a simple backup. Serve one Kango process per database so browser revision updates stay synchronized.

The Go binary embeds templates, CSS, HTMX 2.0.8, and SortableJS 1.15.6. Restart `go run .` after changing source or assets. No Node build is required. Third-party asset licenses are in `web/static/LICENSES.txt`.

`go test -race ./...` covers persistence, ordering and rollback, transfers, independent statuses, card content, relationships, attachments, cascading deletes, server-rendered views, form updates, Markdown safety, origin checks, and a real Streamable HTTP MCP client connection.
